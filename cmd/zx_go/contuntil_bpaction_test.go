package main

import (
	"strings"
	"testing"
)

// iter 336: cover cmdContUntil (arm / inspect / clear / bad-expr) plus
// the runBPAction dispatch loop and its oneLine / formatHex16 helpers.

func TestCmdContUntil(t *testing.T) {
	d := &remoteDebugger{}

	// No args, nothing armed.
	if got := d.cmdContUntil(nil); got != "OK (no cont-until armed)" {
		t.Errorf("inspect-empty = %q", got)
	}
	// Bad expression.
	if got := d.cmdContUntil([]string{"@@@"}); !strings.HasPrefix(got, "ERR condition") {
		t.Errorf("bad expr = %q", got)
	}
	// Arm a real condition — also clears paused.
	d.paused.Store(true)
	got := d.cmdContUntil([]string{"sp", "<", "$2000"})
	if !strings.HasPrefix(got, "OK cont-until") || !strings.HasSuffix(got, "running") {
		t.Errorf("arm = %q", got)
	}
	if d.paused.Load() {
		t.Error("arm should clear paused")
	}
	// Inspect while armed.
	if got := d.cmdContUntil(nil); !strings.Contains(got, "(armed)") {
		t.Errorf("inspect-armed = %q", got)
	}
	// Clear.
	if got := d.cmdContUntil([]string{"clear"}); got != "OK cont-until cleared" {
		t.Errorf("clear = %q", got)
	}
	if d.contUntilCond.Load() != nil {
		t.Error("clear should nil the condition")
	}
	if got := d.cmdContUntil([]string{"off"}); got != "OK cont-until cleared" {
		t.Errorf("off = %q", got)
	}
}

func TestRunBPAction(t *testing.T) {
	d := newRemoteWithCPU(t)
	// Empty / whitespace-only commands are skipped (no panic).
	d.runBPAction(0x1234, " ; ;  ")
	// A real command runs through handleCommand and emits a bp-action
	// log line. list-breakpoints needs only the bpsMap (nil → "no
	// breakpoints set"), so it dispatches cleanly on the fixture.
	d.runBPAction(0x4000, "list-breakpoints")
}
