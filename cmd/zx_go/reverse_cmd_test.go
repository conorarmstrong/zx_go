package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// Reverse debugging through the telnet dispatch. The cursor logic itself
// is proven in pkg/debugger; what these cover is the part a user can
// reach: that each command is routed, that every error the cursor can
// return arrives as a message rather than a silent no-op, that a
// historical register dump is unmistakably marked as historical, and
// that running the machine forward drops a cursor whose ring is being
// overwritten underneath it.

// newRemoteWithHistory wires a remoteDebugger over a populated M1 ring.
// Entry i carries PC=$8000+i, A=i and insn=i, so a test can name the
// instant it expects to land on. The live CPU is parked at $1234 —
// an address no ring entry holds — so "did this read the recording or
// the live CPU?" is answerable from the output alone.
func newRemoteWithHistory(t *testing.T, n int, wide bool) *remoteDebugger {
	t.Helper()
	d := newRemoteWithCPU(t)
	if wide {
		d.history = debugger.NewHistoryWide(n)
	} else {
		d.history = debugger.NewHistory(n)
	}
	for i := 0; i < n; i++ {
		d.history.Push(debugger.HistoryEntry{
			PC:    uint16(0x8000 + i),
			SP:    0xFF00,
			A:     byte(i),
			BC:    uint16(i * 2),
			Insns: uint64(i),
		})
	}
	d.emu.cpu.PC = 0x1234
	d.paused.Store(true) // skip the implicit pause-ack wait
	return d
}

func TestStepBack_MovesOneInstantIntoThePast(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("step-back")
	if !strings.HasPrefix(got, "OK ") {
		t.Fatalf("step-back = %q, want OK", got)
	}
	if !strings.Contains(got, "[REVERSE -1]") {
		t.Errorf("step-back = %q, want it marked [REVERSE -1]", got)
	}
	if !strings.Contains(got, "insn=8") {
		t.Errorf("step-back = %q, want the instant before the newest (insn=8)", got)
	}
}

func TestStepBack_TakesACount(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("step-back 4")
	if !strings.Contains(got, "[REVERSE -4]") || !strings.Contains(got, "insn=5") {
		t.Errorf("step-back 4 = %q, want [REVERSE -4] at insn=5", got)
	}
}

func TestStepBack_RejectsANonCount(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	for _, arg := range []string{"0", "-2", "zz"} {
		if got := d.handleCommand("step-back " + arg); !strings.HasPrefix(got, "ERR usage") {
			t.Errorf("step-back %s = %q, want ERR usage", arg, got)
		}
	}
}

// The oldest entry is a wall. Walking into it must say so, and must say
// where the cursor actually ended up — a partial move reported as a
// success would leave the user reading an instant they did not ask for.
func TestStepBack_ReportsRunningOutOfHistory(t *testing.T) {
	d := newRemoteWithHistory(t, 3, true)

	got := d.handleCommand("step-back 5")
	if !strings.HasPrefix(got, "ERR") {
		t.Fatalf("step-back past the oldest entry = %q, want ERR", got)
	}
	if !strings.Contains(got, "start of recorded history") {
		t.Errorf("step-back = %q, want the start-of-history reason", got)
	}
	if !strings.Contains(got, "moved 2 of 5") {
		t.Errorf("step-back = %q, want it to report how far it actually moved", got)
	}
	if !strings.Contains(got, "[REVERSE -2]") {
		t.Errorf("step-back = %q, want the resting cursor position", got)
	}
}

func TestReverseCommands_ReportHistoryDisabled(t *testing.T) {
	d := newRemoteWithCPU(t) // no ring
	d.paused.Store(true)
	for _, line := range []string{"step-back", "step-forward", "run-back a == 1", "reverse-status"} {
		got := d.handleCommand(line)
		if !strings.HasPrefix(got, "ERR history disabled") {
			t.Errorf("%q with no ring = %q, want ERR history disabled", line, got)
		}
	}
}

func TestStepBack_ReportsAnEmptyRing(t *testing.T) {
	d := newRemoteWithHistory(t, 0, true)
	d.history = debugger.NewHistoryWide(8) // allocated but never pushed

	got := d.handleCommand("step-back")
	if !strings.HasPrefix(got, "ERR") || !strings.Contains(got, "no instruction history recorded") {
		t.Errorf("step-back over an empty ring = %q, want the no-history reason", got)
	}
}

func TestStepForward_RefusesToRunTheMachine(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("step-forward")
	if !strings.HasPrefix(got, "ERR") || !strings.Contains(got, "already at the present") {
		t.Errorf("step-forward at the present = %q, want the at-present reason", got)
	}
}

func TestStepForward_ReturnsToThePresent(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 3")

	got := d.handleCommand("step-forward 3")
	if !strings.HasPrefix(got, "OK ") || !strings.Contains(got, "at the present") {
		t.Fatalf("step-forward 3 = %q, want a return to the present", got)
	}
	// Back at the present, reads must be live again.
	if regs := d.handleCommand("get-registers"); !strings.HasPrefix(regs, "OK PC=$1234") {
		t.Errorf("get-registers after returning to the present = %q, want the live CPU", regs)
	}
}

func TestStepForward_ReportsOvershootingThePresent(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 1")

	got := d.handleCommand("step-forward 3")
	if !strings.HasPrefix(got, "ERR") || !strings.Contains(got, "already at the present") {
		t.Fatalf("step-forward past the present = %q, want ERR", got)
	}
	if !strings.Contains(got, "moved 1 of 3") {
		t.Errorf("step-forward = %q, want it to report how far it actually moved", got)
	}
}

func TestRunBack_StopsAtTheMatchingInstant(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("run-back a == 3")
	if !strings.HasPrefix(got, "OK ") {
		t.Fatalf("run-back = %q, want OK", got)
	}
	if !strings.Contains(got, "insn=3") || !strings.Contains(got, "[REVERSE -6]") {
		t.Errorf("run-back a == 3 = %q, want [REVERSE -6] at insn=3", got)
	}
}

// The documented invocation quotes the expression. Accept it.
func TestRunBack_AcceptsAQuotedExpression(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	if got := d.handleCommand(`run-back "a == 3"`); !strings.Contains(got, "insn=3") {
		t.Errorf("run-back with a quoted expression = %q, want it to parse", got)
	}
}

func TestRunBack_ReportsNoEarlierMatch(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("run-back a == $FE")
	if !strings.HasPrefix(got, "ERR") || !strings.Contains(got, "no earlier instant") {
		t.Fatalf("run-back with no match = %q, want the no-match reason", got)
	}
	// A search that found nothing must not have moved the cursor.
	if !strings.Contains(got, "at the present") {
		t.Errorf("run-back = %q, want it to say the cursor did not move", got)
	}
}

// The refusal that keeps reverse mode honest: memory is not recorded per
// instruction, so a memory-reading condition has no answer going
// backwards and must be reported as an error rather than evaluated
// against a zero that was never read.
func TestRunBack_RefusesAMemoryReadingCondition(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	got := d.handleCommand("run-back read $5C78 == 0")
	if !strings.HasPrefix(got, "ERR") {
		t.Fatalf("run-back on a memory condition = %q, want an ERR — evaluating it "+
			"against zero would make it a different condition than the one written", got)
	}
	if !strings.Contains(got, "not recorded per instruction") {
		t.Errorf("run-back = %q, want the reason the condition cannot be answered", got)
	}
}

func TestRunBack_RefusesARegisterANarrowRingNeverRecorded(t *testing.T) {
	d := newRemoteWithHistory(t, 6, false) // narrow: no BC/DE/HL/IX/IY

	got := d.handleCommand("run-back b == 0")
	if !strings.HasPrefix(got, "ERR") || !strings.Contains(got, "not recorded per instruction") {
		t.Errorf("run-back on an unrecorded register = %q, want it refused", got)
	}
}

func TestRunBack_ReportsABadExpression(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	if got := d.handleCommand("run-back a =="); !strings.HasPrefix(got, "ERR run-back condition") {
		t.Errorf("run-back with a broken expression = %q, want a parse error", got)
	}
	if got := d.handleCommand("run-back"); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("run-back with no expression = %q, want ERR usage", got)
	}
}

// While the cursor is away from the present, a register dump must read
// the recording and must be unmistakably marked, so nobody reads a
// historical dump as the live machine.
func TestReverseMode_RegisterDumpReadsTheRecordingAndSaysSo(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 2")

	got := d.handleCommand("get-registers")
	if !strings.Contains(got, "[REVERSE -2]") {
		t.Errorf("get-registers in reverse mode = %q, want it marked [REVERSE -2]", got)
	}
	if !strings.Contains(got, "$8007") {
		t.Errorf("get-registers in reverse mode = %q, want the PC under the cursor ($8007)", got)
	}
	if strings.Contains(got, "$1234") {
		t.Errorf("get-registers in reverse mode = %q, must NOT report the live PC", got)
	}
}

func TestReverseMode_HistoryAndDisassembleFollowTheCursor(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 2")

	hist := d.handleCommand("history")
	if !strings.Contains(hist, "[REVERSE -2]") || !strings.Contains(hist, "insn=7") {
		t.Errorf("history in reverse mode = %q, want the cursor position and its instant", hist)
	}
	dis := d.handleCommand("disassemble")
	if !strings.Contains(dis, "[REVERSE -2]") {
		t.Errorf("disassemble in reverse mode = %q, want it marked reverse", dis)
	}
	if !strings.Contains(dis, "8007") {
		t.Errorf("disassemble in reverse mode = %q, want it to start at the PC under the cursor", dis)
	}
}

func TestToPresent_LeavesReverseMode(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 3")

	got := d.handleCommand("to-present")
	if !strings.HasPrefix(got, "OK ") || !strings.Contains(got, "-3") {
		t.Fatalf("to-present = %q, want an OK naming where it came from", got)
	}
	if d.reverse != nil {
		t.Error("to-present must drop the cursor")
	}
	if regs := d.handleCommand("get-registers"); !strings.HasPrefix(regs, "OK PC=$1234") {
		t.Errorf("get-registers after to-present = %q, want the live CPU", regs)
	}
}

func TestToPresent_WhenNotInReverseMode(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	if got := d.handleCommand("to-present"); !strings.HasPrefix(got, "OK already at the present") {
		t.Errorf("to-present with no cursor = %q", got)
	}
}

func TestReverseStatus_ReportsBothStates(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)

	off := d.handleCommand("reverse-status")
	if !strings.Contains(off, "reverse off") || !strings.Contains(off, "entries=10") {
		t.Errorf("reverse-status at the present = %q", off)
	}
	d.handleCommand("step-back 4")
	on := d.handleCommand("reverse-status")
	if !strings.Contains(on, "reverse on") || !strings.Contains(on, "[REVERSE -4]") {
		t.Errorf("reverse-status in reverse mode = %q", on)
	}
	if !strings.Contains(on, "insn=5") {
		t.Errorf("reverse-status = %q, want the instant under the cursor", on)
	}
}

// The ring is overwritten while the machine runs, so a cursor cannot
// survive forward motion: its distance from the newest entry stops
// naming the instruction the user was reading.
func TestForwardMotionDropsTheReverseCursor(t *testing.T) {
	d := newRemoteWithHistory(t, 10, true)
	d.handleCommand("step-back 3")
	if d.reverse == nil {
		t.Fatal("step-back should have created a cursor")
	}

	d.handleCommand("continue")
	if d.reverse != nil {
		t.Error("running the machine forward must invalidate the reverse cursor")
	}

	d.paused.Store(true) // `continue` cleared it; no headless loop here to re-pause
	if got := d.handleCommand("get-registers"); !strings.HasPrefix(got, "OK PC=$1234") {
		t.Errorf("get-registers after continue = %q, want the live CPU", got)
	}
}

func TestCommandsInvalidatingReverse_CoversEveryForwardMove(t *testing.T) {
	// Every command that lets the CPU advance (or that discards the state
	// the ring describes) must drop the cursor, aliases included.
	for _, cmd := range []string{
		"continue", "cont", "c", "run", "r",
		"step", "s",
		"step-over", "n", "next",
		"forward", "f",
		"cold-reset", "tt-rewind",
	} {
		if !commandsInvalidatingReverse[cmd] {
			t.Errorf("commandsInvalidatingReverse[%q] = false, want true "+
				"(the ring is overwritten underneath the cursor)", cmd)
		}
	}
}

func TestReverseCommands_ImplicitlyPause(t *testing.T) {
	// These read the ring, whose contents shift under a running CPU.
	for _, cmd := range []string{"step-back", "step-forward", "run-back", "reverse-status"} {
		if !commandsNeedingPause[cmd] {
			t.Errorf("commandsNeedingPause[%q] = false, want true", cmd)
		}
	}
}

func TestHelpListsTheReverseCommands(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true)
	help := d.handleCommand("help")
	for _, cmd := range []string{"step-back", "step-forward", "run-back", "to-present", "reverse-status"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help does not list %q", cmd)
		}
	}
}
