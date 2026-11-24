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
	{"gui", func(e *emulator, _ roms.SpectrumModel) { e.runGUIFrame(nil) }},
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

	// Count the instants the next two frames pass: each instruction boundary
	// the debugger could have stopped at, and the end of each frame. A
	// frame's first boundary is the instant before it when no interrupt was
	// taken at the frame start, and counts once.
	n := newInstantCounter(emu)
	emu.cpu.BreakpointCheck = n.check(0)
	tick(emu, model)
	n.frameEnd()
	const stopAfter = 300
	emu.cpu.BreakpointCheck = n.check(stopAfter)
	tick(emu, model)
	emu.cpu.BreakpointCheck = nil
	if !n.stopped {
		t.Fatalf("the fifth frame was not stopped at its boundary %d", stopAfter+1)
	}
	if bytes.Equal(reg.Capture().Bytes(), passed.Bytes()) {
		t.Fatal("the machine did not move after the capture; the fixture proves nothing")
	}

	back := n.instants
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

// instantCounter counts the instants a run passes, as replay numbers them.
type instantCounter struct {
	emu      *emulator
	last     replayPoint
	instants int
	calls    int
	stopped  bool
}

func newInstantCounter(e *emulator) *instantCounter {
	return &instantCounter{emu: e, last: here(e)}
}

func here(e *emulator) replayPoint {
	return replayPoint{tstates: e.cpu.Tstates(), insn: e.cpu.InstructionCount()}
}

// check returns a BreakpointCheck that counts distinct instants and, when
// stopAfter is positive, stops the frame at the boundary after that many calls.
// The stopping boundary is an instant of its own: it is where the frame ends.
func (n *instantCounter) check(stopAfter int) func(uint16) bool {
	n.calls = 0
	return func(uint16) bool {
		n.calls++
		at := here(n.emu)
		if at != n.last {
			n.instants++
			n.last = at
		}
		if stopAfter > 0 && n.calls > stopAfter {
			n.stopped = true
			return true
		}
		return false
	}
}

// frameEnd counts the end of a frame that ran whole.
func (n *instantCounter) frameEnd() {
	if at := here(n.emu); at != n.last {
		n.instants++
		n.last = at
	}
}

// Landing inside a frame the headless loop ran is landing on the live machine:
// that loop does nothing after a frame, so stopping a frame at a boundary leaves
// the machine exactly as the live run had it there. The capture taken inside the
// live frame, at that boundary, is the oracle. (The GUI loop renders after a
// frame, so a landing inside one of its frames is the machine as a breakpoint
// there leaves it, render included, which is not an instant the live run passed
// through.)
func TestReplayStepBackLandsInsideAHeadlessFrame(t *testing.T) {
	for _, model := range []roms.SpectrumModel{roms.Model48K, roms.Model128K} {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			emu, tt := armReplayFixture(t, model, 20000, 16)
			for i := 0; i < 3; i++ {
				runOneFrameHeadless(emu, model)
			}
			reg := emu.stateRegistry()
			const at, stopAfter = 120, 400
			var passed []byte
			var passedAt uint64
			n := newInstantCounter(emu)
			inner := n.check(stopAfter)
			emu.cpu.BreakpointCheck = func(pc uint16) bool {
				stop := inner(pc)
				if n.calls == at {
					passed = reg.Capture().Bytes()
					passedAt = emu.cpu.InstructionCount()
				}
				return stop
			}
			runOneFrameHeadless(emu, model)
			emu.cpu.BreakpointCheck = nil
			if passed == nil || !n.stopped {
				t.Fatal("the fixture did not reach its capture and stop boundaries")
			}
			// From the stop back to boundary `at`: one instant per distinct
			// boundary in between. The counter numbers instants from the
			// frame start, so the distance is the count at the stop less the
			// count at `at`.
			back := stopAfter + 1 - at
			landed, err := tt.replayBack(back)
			if err != nil {
				t.Fatalf("replay-back %d: %v", back, err)
			}
			if landed != passedAt {
				t.Errorf("landed on instruction %d, want %d", landed, passedAt)
			}
			if !bytes.Equal(reg.Capture().Bytes(), passed) {
				t.Error("landed on the boundary but not on the machine the live frame had there")
			}
		})
	}
}

// One step back from a machine stopped inside a frame lands on the boundary
// before it, not on the same instant.
func TestReplayOneStepBackMovesOneBoundary(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 20000, 16)
	for i := 0; i < 3; i++ {
		runOneFrameHeadless(emu, roms.Model128K)
	}
	var prev, cur replayPoint
	calls := 0
	emu.cpu.BreakpointCheck = func(uint16) bool {
		calls++
		prev, cur = cur, here(emu)
		return calls > 250
	}
	runOneFrameHeadless(emu, roms.Model128K)
	emu.cpu.BreakpointCheck = nil
	if here(emu) != cur {
		t.Fatalf("the frame stopped at %+v, not at the boundary that stopped it, %+v", here(emu), cur)
	}
	if _, err := tt.replayBack(1); err != nil {
		t.Fatalf("replay-back 1: %v", err)
	}
	if got := here(emu); got != prev {
		t.Errorf("one step back landed on %+v, want the previous boundary %+v", got, prev)
	}
}

// After stepping back into a frame the loop ran, the next step back walks from
// there: the walk proves itself against the new present each time.
func TestReplayStepBackTwiceInsideAFrame(t *testing.T) {
	emu, tt := armReplayFixture(t, roms.Model128K, 20000, 16)
	for i := 0; i < 3; i++ {
		emu.runGUIFrame(nil)
	}
	calls := 0
	emu.cpu.BreakpointCheck = func(uint16) bool { calls++; return calls > 200 }
	emu.runGUIFrame(nil)
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
		emu.runGUIFrame(nil)
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
		emu.runGUIFrame(nil)
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
