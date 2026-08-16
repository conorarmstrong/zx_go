package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// Reverse debugging over the telnet protocol: `step-back`, `step-forward`,
// `run-back`, `to-present`, `reverse-status`. The cursor and its semantics
// live in pkg/debugger/reverse.go; this file is the front-end.
//
// The machine does not run backwards. A cursor moves through the recorded
// M1 instants, and while it is away from the present get-registers /
// history / disassemble read the instant under it rather than the live
// CPU. Every such response carries a [REVERSE -N] tag: a historical
// register dump mistaken for a live one is worse than no reverse mode.
//
// What the recording cannot give is memory, which is not captured per
// instruction. `run-back read $5C78 == 0` is therefore refused rather
// than evaluated against a zero that was never read.

// commandsInvalidatingReverse lists the commands after which the cursor
// stops meaning anything, so it is dropped BEFORE they run.
//
// Two reasons, both fatal to a cursor. Forward motion overwrites the
// ring: the cursor's distance-from-newest no longer names the
// instruction the user was reading, and the instant it pointed at may
// have been overwritten entirely. A rewind (tt-rewind) or a cold reset
// discards the machine state the recording describes, so the newest ring
// entry is no longer the present.
//
// Dropping is the same as resetting to the present — the next reverse
// command re-derives a cursor from the newest entry, which is the only
// position still meaningful.
var commandsInvalidatingReverse = map[string]bool{
	"continue": true, "cont": true, "c": true, "run": true, "r": true,
	"step": true, "s": true,
	"step-over": true, "n": true, "next": true,
	"forward": true, "f": true,
	"cold-reset": true,
	"tt-rewind":  true,
}

// reverseCursor returns the live cursor, creating one at the newest
// recorded instant on first use. The second return is a protocol error
// string, non-empty when there is nothing to walk.
func (d *remoteDebugger) reverseCursor() (*debugger.ReverseCursor, string) {
	if d.history == nil {
		return nil, "ERR history disabled (relaunch with --debugger-history=N; add --debugger-history-wide to run backwards to a BC/DE/HL/IX/IY condition)"
	}
	if d.reverse == nil {
		c := debugger.NewReverseCursor(d.history)
		if !c.Active() {
			return nil, "ERR " + debugger.ErrNoHistory.Error() + " (the ring is empty — run the machine first)"
		}
		d.reverse = c
	}
	return d.reverse, ""
}

// reverseInstant is the recorded instant under the cursor, and false when
// the debugger is at the present and reads should go to the live machine.
func (d *remoteDebugger) reverseInstant() (debugger.HistoryEntry, bool) {
	if d.reverse == nil || d.reverse.AtPresent() {
		return debugger.HistoryEntry{}, false
	}
	return d.reverse.Current()
}

// reverseTag marks a response as historical. Every reverse-mode read
// carries it.
func (d *remoteDebugger) reverseTag() string {
	if d.reverse == nil {
		return ""
	}
	return fmt.Sprintf("[REVERSE -%d]", d.reverse.Distance())
}

// reverseWhere describes where the cursor sits, in words when it is back
// at the live machine.
func (d *remoteDebugger) reverseWhere() string {
	if d.reverse == nil || d.reverse.AtPresent() {
		return "at the present (live machine)"
	}
	return d.reverseTag()
}

// fmtHistoryInstant renders one recorded instant on a single line, using
// the same columns and order as `prev`. A narrow ring says outright that
// the pair registers were never recorded, rather than printing the zeros
// those fields still hold.
func fmtHistoryInstant(e debugger.HistoryEntry, wide bool) string {
	src := ""
	if e.Source != debugger.SourceSequential {
		if e.SourceFrom != 0 {
			src = fmt.Sprintf(" via=%s@$%04X", e.Source, e.SourceFrom)
		} else {
			src = fmt.Sprintf(" via=%s", e.Source)
		}
	}
	if wide {
		return fmt.Sprintf(
			"insn=%d %s SP=$%04X AF=$%02X%02X BC=$%04X DE=$%04X HL=$%04X IX=$%04X IY=$%04X IFF1=%v IM=%d Halted=%v bank=%d%s",
			e.Insns, annotateAddr(e.PC), e.SP, e.A, e.F,
			e.BC, e.DE, e.HL, e.IX, e.IY,
			e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, src)
	}
	return fmt.Sprintf(
		"insn=%d %s SP=$%04X AF=$%02X%02X IFF1=%v IM=%d Halted=%v bank=%d%s (narrow ring: BC/DE/HL/IX/IY not recorded)",
		e.Insns, annotateAddr(e.PC), e.SP, e.A, e.F,
		e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, src)
}

// reverseRegisters is the get-registers redirect. Returns false when the
// cursor is at the present and the live dump should be used instead.
//
// I and R are absent because the ring does not record them; saying so is
// cheaper than letting someone read a stale value off a live dump.
func (d *remoteDebugger) reverseRegisters() (string, bool) {
	e, ok := d.reverseInstant()
	if !ok {
		return "", false
	}
	return fmt.Sprintf("OK %s %s (recorded instant, NOT the live CPU; I and R are not recorded)",
		d.reverseTag(), fmtHistoryInstant(e, d.history.Wide())), true
}

// reverseAt formats the cursor's resting place after a successful move.
func (d *remoteDebugger) reverseAt() string {
	e, ok := d.reverseInstant()
	if !ok {
		return "OK at the present (live machine); reverse mode off"
	}
	return fmt.Sprintf("OK %s %s", d.reverseTag(), fmtHistoryInstant(e, d.history.Wide()))
}

// parseReverseCount reads the optional instruction count shared by
// step-back / step-forward. Default 1.
func parseReverseCount(args []string, cmd string) (int, string) {
	if len(args) == 0 {
		return 1, ""
	}
	v, err := strconv.Atoi(args[0])
	if err != nil || v < 1 {
		return 0, fmt.Sprintf("ERR usage: %s [N]   (N >= 1, default 1)", cmd)
	}
	return v, ""
}

// cmdStepBack moves the cursor N instructions into the past.
//
// Running out of recorded history part-way through is reported, along
// with how far the cursor actually got: a partial move announced as a
// success would leave the user reading an instant they did not ask for.
func (d *remoteDebugger) cmdStepBack(args []string) string {
	n, errStr := parseReverseCount(args, "step-back")
	if errStr != "" {
		return errStr
	}
	c, errStr := d.reverseCursor()
	if errStr != "" {
		return errStr
	}
	for i := 0; i < n; i++ {
		if err := c.StepBack(); err != nil {
			return fmt.Sprintf("ERR step-back: %v (moved %d of %d; cursor %s)",
				err, i, n, d.reverseWhere())
		}
	}
	return d.reverseAt()
}

// cmdStepForward moves the cursor N instructions towards the present.
// Arriving at the present drops the cursor: reads go live again.
func (d *remoteDebugger) cmdStepForward(args []string) string {
	n, errStr := parseReverseCount(args, "step-forward")
	if errStr != "" {
		return errStr
	}
	c, errStr := d.reverseCursor()
	if errStr != "" {
		return errStr
	}
	for i := 0; i < n; i++ {
		if err := c.StepForward(); err != nil {
			msg := fmt.Sprintf("ERR step-forward: %v (moved %d of %d; cursor %s)",
				err, i, n, d.reverseWhere())
			d.dropCursorAtPresent()
			return msg
		}
	}
	out := d.reverseAt()
	d.dropCursorAtPresent()
	return out
}

// dropCursorAtPresent releases a cursor that has walked back to the
// newest recorded instant. Holding one there would be a reverse mode
// that is not reverse: the present is the live machine.
func (d *remoteDebugger) dropCursorAtPresent() {
	if d.reverse != nil && d.reverse.AtPresent() {
		d.reverse = nil
	}
}

// cmdRunBack searches backwards for the most recent earlier instant
// satisfying a breakpoint-grammar condition.
//
// Three failures are all reported rather than smoothed over: no earlier
// match, and — the two that matter — a condition reading memory or a
// register this ring never recorded, which have no answer going
// backwards. See pkg/debugger/reverse.go for why they are refused
// instead of evaluated.
func (d *remoteDebugger) cmdRunBack(args []string) string {
	if len(args) == 0 {
		return "ERR usage: run-back EXPR   (breakpoint-condition grammar, e.g. `run-back a == $FF`)"
	}
	// The documented invocation quotes the expression (`run-back "a == 3"`),
	// matching the `do "..."` clause of set-breakpoint. Strip one
	// surrounding pair so both forms parse.
	expr := strings.TrimSpace(strings.Join(args, " "))
	if len(expr) >= 2 && expr[0] == '"' && expr[len(expr)-1] == '"' {
		expr = expr[1 : len(expr)-1]
	}
	cond, err := debugger.ParseCondition(expr)
	if err != nil {
		return "ERR run-back condition: " + err.Error()
	}
	c, errStr := d.reverseCursor()
	if errStr != "" {
		return errStr
	}
	if err := c.RunBack(cond); err != nil {
		return fmt.Sprintf("ERR run-back: %v (cursor unmoved, %s)", err, d.reverseWhere())
	}
	return d.reverseAt()
}

// cmdToPresent leaves reverse mode and hands reads back to the live
// machine.
func (d *remoteDebugger) cmdToPresent() string {
	if d.reverse == nil || d.reverse.AtPresent() {
		d.reverse = nil
		return "OK already at the present (not in reverse mode)"
	}
	back := d.reverse.Distance()
	d.reverse.ToPresent()
	d.reverse = nil
	return fmt.Sprintf("OK left reverse mode (was -%d); reads are live again", back)
}

// cmdReverseStatus reports whether reverse mode is active, how far back
// the cursor sits, and how far back it could go.
func (d *remoteDebugger) cmdReverseStatus() string {
	if d.history == nil {
		return "ERR history disabled (relaunch with --debugger-history=N)"
	}
	// The oldest reachable instant is one short of the ring's fill: the
	// newest entry is the present, distance 0.
	oldest := d.history.Len() - 1
	if oldest < 0 {
		oldest = 0
	}
	ring := fmt.Sprintf("ring entries=%d capacity=%d wide=%v oldest-reachable=-%d",
		d.history.Len(), d.history.Capacity(), d.history.Wide(), oldest)
	e, ok := d.reverseInstant()
	if !ok {
		return "OK reverse off (at the present, reads are live); " + ring
	}
	return fmt.Sprintf("OK reverse on %s; %s; cursor: %s",
		d.reverseTag(), ring, fmtHistoryInstant(e, d.history.Wide()))
}
