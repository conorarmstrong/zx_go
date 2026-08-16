package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// `replay-back` and `step-back` both move backwards and give different things,
// so the surface has to keep them apart. Two commands, and a help line that
// says which one carries memory: a user who reads `step-back` as "go back one
// instruction" and then trusts a memory dump has been misled by us.

func TestHelpDistinguishesTheTwoWaysOfGoingBack(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true)
	help := d.handleCommand("help")
	for _, cmd := range []string{"step-back", "replay-back"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help does not list %q", cmd)
		}
	}
	// The distinction, not just the names. Both words have to be in there or
	// the two commands read as synonyms.
	for _, phrase := range []string{"memory", "registers"} {
		if !strings.Contains(help, phrase) {
			t.Errorf("help never mentions %q, so nothing says which command gives what", phrase)
		}
	}
}

// Replay re-executes from a checkpoint, so with no checkpoint ring there is
// nothing to re-execute from — and the error has to say how to get one.
func TestReplayBackNeedsTheCheckpointRing(t *testing.T) {
	d := newRemoteWithCPU(t)
	got := d.cmdReplayBack(nil)
	if !strings.HasPrefix(got, "ERR") {
		t.Fatalf("replay-back with no ring = %q, want an error", got)
	}
	if !strings.Contains(got, "tt-on") {
		t.Errorf("the error does not say how to turn checkpoints on: %q", got)
	}
}

func TestReplayBackRejectsANonNumericCount(t *testing.T) {
	d := newRemoteWithCPU(t)
	if got := d.cmdReplayBack([]string{"banana"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("replay-back banana = %q, want an error", got)
	}
	if got := d.cmdReplayBack([]string{"0"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("replay-back 0 = %q, want an error", got)
	}
}

// The command end to end: a machine that has been running, a ring of
// checkpoints, and a step back that lands on the machine execution passed
// through — measured the same way as the buffer-level test, because the
// command's promise is the buffer's promise.
func TestReplayBackCommandStepsTheWholeMachineBack(t *testing.T) {
	emu, _ := armReplayFixture(t, roms.Model128K, 250, 8)
	for i := 0; i < 2000; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	reg := emu.stateRegistry()
	want := reg.Capture()
	wantInsn := emu.cpu.InstructionCount()
	for i := 0; i < 9; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}

	d := &remoteDebugger{emu: emu}
	got := d.cmdReplayBack([]string{"9"})
	if !strings.HasPrefix(got, "OK") {
		t.Fatalf("replay-back 9 = %q", got)
	}
	if !strings.Contains(got, fmt.Sprintf("insn=%d", wantInsn)) {
		t.Errorf("response does not name the instruction it landed on (%d): %q", wantInsn, got)
	}
	if state := reg.Capture(); !bytes.Equal(state.Bytes(), want.Bytes()) {
		t.Error("replay-back returned OK but the machine is not the one execution passed through")
	}
}

// Moving the machine invalidates the recorded-history cursor: it is a distance
// back from the newest ring entry, and after a replay that entry is no longer
// the present. Reading it afterwards would report an instant from a timeline
// the machine has left.
func TestReplayBackDropsTheRecordedHistoryCursor(t *testing.T) {
	if !commandsInvalidatingReverse["replay-back"] {
		t.Error("commandsInvalidatingReverse[\"replay-back\"] = false: a replay leaves a stale " +
			"reverse cursor pointing into a timeline the machine has left")
	}
	if !commandsNeedingPause["replay-back"] {
		t.Error("commandsNeedingPause[\"replay-back\"] = false: replaying under a running CPU " +
			"restores a checkpoint the emulation goroutine is mid-frame in")
	}
}
