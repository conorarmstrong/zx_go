package main

import (
	"fmt"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// cmdStepOver implements `step-over` / `n` / `next`. If the
// instruction at PC pushes a return address (CALL, conditional
// CALL, RST, Z80N PUSH NN), a one-shot tentative breakpoint is
// planted at PC+instruction-length and the CPU runs until it
// fires. Otherwise the command degenerates to a plain step.
//
// The one-shot BP lives on remoteDebugger.stepOverPC; BreakpointCheck
// fires once at the matching PC then clears the pointer so a
// subsequent `continue` starts clean.
func (d *remoteDebugger) cmdStepOver() string {
	c := d.emu.cpu
	mem := d.emu.mem
	read := func(a uint16) byte { return mem.Read(a) }
	lines := debugger.Disassemble(read, c.PC, 1)
	if len(lines) == 0 || len(lines[0].Bytes) == 0 {
		// Fall back to plain step if we can't decode the instruction.
		return d.runStep()
	}
	op := lines[0].Bytes[0]
	if !isCallLike(op, lines[0].Bytes) {
		return d.runStep()
	}
	target := c.PC + uint16(len(lines[0].Bytes))
	d.stepOverPC.Store(&target)
	// Resume just like a plain `continue` — same mechanism, except
	// BreakpointCheck will halt as soon as the one-shot fires.
	d.paused.Store(false)
	d.stepping.Store(false)
	select {
	case d.resumeCh <- struct{}{}:
	default:
	}
	// Wait for the next pause-ack (the one-shot fires, paused.Store
	// is set, headless loop hits WaitIfPaused → closes pauseAckCh).
	if !d.waitForPauseAck() {
		// Step-over didn't complete in time; clear the pending bp so
		// it doesn't fire later out of context.
		d.stepOverPC.Store(nil)
		return "ERR step-over timed out"
	}
	return fmt.Sprintf("OK stepped over to $%04X", target)
}

// runStep mirrors the plain `step` command's blocking semantics so
// step-over can degrade to step without duplicating the channel
// dance.
func (d *remoteDebugger) runStep() string {
	select {
	case <-d.stepDoneCh:
	default:
	}
	d.stepping.Store(true)
	select {
	case d.resumeCh <- struct{}{}:
	default:
	}
	select {
	case <-d.stepDoneCh:
	case <-time.After(2 * time.Second):
		return "ERR step timed out"
	}
	return "OK stepped"
}

// isCallLike reports whether the given opcode pushes a return
// address — the operation a `step-over` should skip past.
// Covers:
//
//	CALL nn               : 0xCD
//	CALL cc,nn            : 0xC4 / 0xCC / 0xD4 / 0xDC / 0xE4 / 0xEC / 0xF4 / 0xFC
//	RST n                 : 0xC7 / 0xCF / 0xD7 / 0xDF / 0xE7 / 0xEF / 0xF7 / 0xFF
//	Z80N PUSH NN          : 0xED 0x8A
func isCallLike(op byte, bytes []byte) bool {
	switch op {
	case 0xCD:
		return true
	case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC:
		return true
	case 0xC7, 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF:
		return true
	case 0xED:
		return len(bytes) >= 2 && bytes[1] == 0x8A
	}
	return false
}
