package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cmdForward steps N M1 fetches forward, emitting one line per step
// with the same columns as `prev`. Mirror-image of `prev N`: where
// prev shows the recent past, forward shows the immediate future
// from the current PC. Useful after a breakpoint hit to walk a code
// path without bookkeeping a slew of `step; regs` pairs.
//
// Default N=10, capped at 256 to keep the response bounded.
//
// Bank annotation matches `prev`. Symbol-map annotation is applied
// to each PC via annotateAddr.
func (d *remoteDebugger) cmdForward(args []string) string {
	n := 10
	if len(args) >= 1 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 1 {
			return "ERR usage: forward [N]   (default 10, max 256)"
		}
		n = v
	}
	if n > 256 {
		n = 256
	}
	var b strings.Builder
	b.WriteString("OK\r\n")
stepLoop:
	for i := 0; i < n; i++ {
		// Single-step. Same machinery as the `step` command but
		// without the per-step ack overhead — we re-use the
		// stepping flag + resume/done channels.
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
			b.WriteString("  ERR step timed out\r\n")
			break stepLoop
		}
		c := d.emu.cpu
		bank := d.currentROMBank()
		fmt.Fprintf(&b,
			"  insn=%-10d %s  SP=$%04X AF=$%02X%02X BC=$%04X DE=$%04X HL=$%04X  IFF1=%v IM=%d Halted=%v  bank=%d\r\n",
			c.InstructionCount(), annotateAddr(c.PC), c.SP, c.A, c.F,
			c.BC(), c.DE(), c.HL(), c.IFF1, c.IM, c.Halted, bank)
	}
	return strings.TrimRight(b.String(), "\r\n")
}
