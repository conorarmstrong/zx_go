package main

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Replay used to reach back only through spans the debugger single-stepped.
// The frame loop's bulk driver, ExecuteFrame, rebases the T-state counter at
// each frame end and raises the frame interrupt on its own schedule, and the
// GUI loop renders after each frame, which moves the ULA's and the Next's own
// state. Re-running a frame-loop span through the step driver reproduced none
// of that, so every step back after the machine had simply been running was
// refused.
//
// The ring now records each frame-loop turn as it ran and replays it with the
// same code, so a machine that has been running can be stepped back into. The
// oracle here is the machine the live run passed through: a frame end the
// present went past is captured as it happened, and replay must land on it
// byte for byte.

// frameTickers are the two frame-loop turns the ring records: the headless
// loop's, which runs the frame and nothing else, and the GUI loop's, which also
// runs the per-frame work and the render.
var frameTickers = []struct {
	name string
	tick func(*emulator, roms.SpectrumModel)
}{
	{"headless", func(e *emulator, m roms.SpectrumModel) { runOneFrameHeadless(e, m) }},
	{"gui", func(e *emulator, _ roms.SpectrumModel) { e.runGUIFrame() }},
}

func TestReplayStepBackReachesIntoFramesTheLoopRan(t *testing.T) {
	for _, ft := range frameTickers {
		for _, model := range []roms.SpectrumModel{roms.Model48K, roms.Model128K, roms.ModelPlus3} {
			t.Run(ft.name+"/"+roms.GetModelName(model), func(t *testing.T) {
				assertFrameReplayLandsOnAFrameEnd(t, model, ft.tick)
			})
		}
	}
}

func TestReplayStepBackReachesIntoFramesTheLoopRanNext(t *testing.T) {
	if _, err := newEmulator(roms.ModelNext); err != nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	for _, ft := range frameTickers {
		t.Run(ft.name, func(t *testing.T) {
			assertFrameReplayLandsOnAFrameEnd(t, roms.ModelNext, ft.tick)
		})
	}
}

// assertFrameReplayLandsOnAFrameEnd runs three frames, captures the machine at
// the end of the third, runs a fourth whole and a fifth stopped part way by a
// breakpoint, as the debugger stops one, and steps back by exactly the number
// of instants passed since the capture.
func assertFrameReplayLandsOnAFrameEnd(t *testing.T, model roms.SpectrumModel, tick func(*emulator, roms.SpectrumModel)) {
	t.Helper()
	emu, tt := armReplayFixture(t, model, 20000, 16)
	for i := 0; i < 3; i++ {
		tick(emu, model)
	}
	reg := emu.stateRegistry()
	passed := reg.Capture()
	passedAt := emu.cpu.InstructionCount()

	// Count the instruction boundaries the next two frames pass. Each is a
	// place the debugger could have stopped, and each is an instant replay can
	// land on; so is the end of the whole frame.
	calls := 0
	emu.cpu.BreakpointCheck = func(uint16) bool { calls++; return false }
	tick(emu, model)
	inFrame4 := calls
	const stopAfter = 300
	calls = 0
	emu.cpu.BreakpointCheck = func(uint16) bool { calls++; return calls > stopAfter }
	tick(emu, model)
	emu.cpu.BreakpointCheck = nil
	if calls != stopAfter+1 {
		t.Fatalf("the fifth frame passed %d boundaries, want it stopped at %d", calls, stopAfter+1)
	}
	if bytes.Equal(reg.Capture().Bytes(), passed.Bytes()) {
		t.Fatal("the machine did not move after the capture; the fixture proves nothing")
	}

	back := inFrame4 + 1 + stopAfter + 1
	landed, err := tt.replayBack(back)
	if err != nil {
		t.Fatalf("replay-back %d: %v", back, err)
	}
	if landed != passedAt {
		t.Errorf("landed on instruction %d, want %d", landed, passedAt)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), passed.Bytes()) {
		t.Error("replay landed on the instruction but not on the machine the live run passed through")
	}
}

// After stepping back into a frame the loop ran, the next step back walks from
// there: the walk proves itself against the new present each time.
func TestReplayStepBackTwiceInsideAFrame(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 20000, 16)
	for i := 0; i < 3; i++ {
		emu.runGUIFrame()
	}
	calls := 0
	emu.cpu.BreakpointCheck = func(uint16) bool { calls++; return calls > 200 }
	emu.runGUIFrame()
	emu.cpu.BreakpointCheck = nil

	first, err := tt.replayBack(1)
	if err != nil {
		t.Fatalf("first step back: %v", err)
	}
	second, err := tt.replayBack(1)
	if err != nil {
		t.Fatalf("second step back: %v", err)
	}
	if second >= first {
		t.Errorf("the second step back landed on instruction %d, not before the first's %d", second, first)
	}
}

// Frames run by calling ExecuteFrame directly, outside the frame-loop turn the
// ring records, are still an unrecorded driver, and replay must still notice.
func TestReplayStepBackRefusesFramesRunOutsideTheLoop(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 20000, 8)
	for i := 0; i < 3; i++ {
		emu.runGUIFrame()
	}
	for i := 0; i < 2; i++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.Model128K, false))
	}
	reg := emu.stateRegistry()
	present := reg.Capture()
	if landed, err := tt.replayBack(1); err == nil {
		t.Fatalf("frames the ring never saw replayed as if recorded, landing at %d", landed)
	}
	if got := reg.Capture(); !bytes.Equal(got.Bytes(), present.Bytes()) {
		t.Error("a refused step back left the machine somewhere other than the present")
	}
}

// A checkpoint is the whole machine, but consecutive checkpoints share every
// piece of state neither changed. A guest that rewrites the screen and the
// sound chip every frame still leaves most of a 128K untouched between
// captures, so the ring must hold far less than one machine per entry; this is
// what makes the 256-entry default affordable.
func TestTheRingSharesUnchangedMemoryBetweenCheckpoints(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 50000, 64)
	for i := 0; i < 200; i++ {
		emu.runGUIFrame()
	}
	snaps := tt.Snapshots()
	if len(snaps) < 16 {
		t.Fatalf("the fixture took %d checkpoints; too few to measure sharing", len(snaps))
	}
	unshared := 0
	for _, e := range snaps {
		unshared += e.state.Size()
	}
	if held := tt.HeldBytes(); held*4 > unshared {
		t.Errorf("%d checkpoints hold %d bytes against %d unshared: sharing saved less than "+
			"three quarters", len(snaps), held, unshared)
	}
}
