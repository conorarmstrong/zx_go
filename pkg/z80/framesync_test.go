package z80

import "testing"

// A machine whose video timing can change does so at a frame-sync point inside
// the frame, and the frame then ends where the new timing puts the next
// interrupt. FrameSyncFn runs once a frame, at FrameSyncAt, and returns the
// frame's new length (0 for no change).
func TestFrameSyncMovesTheFrameEnd(t *testing.T) {
	cpu, _ := createTestCPU() // all-NOP memory
	cpu.IFF1 = false
	cpu.PC = 0x8000 // RAM: NOPs
	calls, moveTo := 0, uint64(1500)
	cpu.FrameSyncAt = 1000
	cpu.FrameSyncFn = func() uint64 {
		calls++
		at := cpu.Tstates()
		if at < 1000 || at > 1004 {
			t.Errorf("sync ran at %d, want at 1000", at)
		}
		m := moveTo
		moveTo = 0
		return m
	}
	cpu.ExecuteFrame(2000)
	if calls != 1 {
		t.Fatalf("sync ran %d times in a frame, want 1", calls)
	}
	if got := cpu.Clock() - cpu.Tstates(); got != 1500 {
		t.Errorf("the frame ended at %d, want 1500", got)
	}
	cpu.ExecuteFrame(2000)
	if got := cpu.Clock() - cpu.Tstates(); calls != 2 || got != 3500 {
		t.Errorf("an unchanged frame: calls %d, ended at %d, want 3500", calls, got)
	}
}

// A frame stopped part way after its sync moved the end resumes with that end.
func TestFrameSyncMovedEndSurvivesAStop(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.IFF1 = false
	cpu.PC = 0x8000 // RAM: NOPs
	cpu.FrameSyncAt = 1000
	moved := false
	cpu.FrameSyncFn = func() uint64 {
		if moved {
			return 0
		}
		moved = true
		return 1500
	}
	cpu.BreakpointCheck = func(uint16) bool { return cpu.Tstates() >= 1200 }
	cpu.ExecuteFrame(2000)
	cpu.BreakpointCheck = nil
	if got := cpu.Tstates(); got < 1200 || got > 1300 {
		t.Fatalf("stopped at %d, want about 1200", got)
	}
	cpu.ExecuteFrame(2000) // resume
	if got := cpu.Clock() - cpu.Tstates(); got != 1500 {
		t.Errorf("the resumed frame ended at %d, want 1500", got)
	}
}

// The single-step driver keeps the same frame boundaries.
func TestFrameSyncInTheStepDriver(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.IFF1 = false
	cpu.PC = 0x8000 // RAM: NOPs
	cpu.FrameTStates = 2000
	cpu.FrameSyncAt = 1000
	moved := false
	cpu.FrameSyncFn = func() uint64 {
		if moved {
			return 0
		}
		moved = true
		return 1500
	}
	var frames []uint64
	cpu.lineIntHook = nil
	for cpu.Tstates() < 4000 {
		before := cpu.nextFrameBoundary
		cpu.StepInstructionWithIRQ()
		if cpu.nextFrameBoundary != before {
			frames = append(frames, cpu.nextFrameBoundary)
		}
	}
	want := []uint64{2000, 1500, 3500, 5500}
	for i, w := range want {
		if i >= len(frames) || frames[i] != w {
			t.Fatalf("frame ends %v, want %v", frames, want)
		}
	}
}

func syncCPU(t *testing.T) *CPU {
	t.Helper()
	cpu, _ := createTestCPU()
	cpu.IFF1 = false
	cpu.PC = 0x8000 // RAM: NOPs
	cpu.FrameTStates = 2000
	cpu.FrameSyncAt = 1000
	moved := false
	cpu.FrameSyncFn = func() uint64 {
		if moved {
			return 0
		}
		moved = true
		cpu.FrameTStates = 2600 // the new timing's frame
		cpu.LineIntOffsetTstates = 1200
		return 1500
	}
	return cpu
}

// After the sync the frame's line interrupt is the new timing's.
func TestFrameSyncMovesTheLineInterrupt(t *testing.T) {
	cpu := syncCPU(t)
	cpu.LineIntOffsetTstates = 1800 // old timing: past the moved end
	var at []uint64
	cpu.lineIntHook = func() { at = append(at, cpu.Tstates()) }
	cpu.ExecuteFrame(2000)
	if len(at) != 1 || at[0] < 1200 || at[0] > 1204 {
		t.Errorf("line interrupt at %v, want once at 1200", at)
	}
}

// Frames after a moved one run at the new timing's length when an interrupt
// poll walks across them (a long zxnDMA block).
func TestPollIntSourcesAfterAMovedFrame(t *testing.T) {
	cpu := syncCPU(t)
	cpu.BreakpointCheck = func(uint16) bool { return cpu.Tstates() >= 1200 }
	cpu.ExecuteFrame(2000)
	cpu.BreakpointCheck = nil
	cpu.PollIntSources(1500 + 2*2600 + 10)
	if got := cpu.frameOrigin; got != 6700 {
		t.Errorf("frame origin after the poll %d, want 6700 (1500 + 2 x 2600)", got)
	}
}

// A frame ExecuteFrame stopped after its sync, stepped past its end, then run
// again: the next frame starts at the moved end.
func TestFrameSyncAcrossBothDrivers(t *testing.T) {
	cpu := syncCPU(t)
	cpu.BreakpointCheck = func(uint16) bool { return cpu.Tstates() >= 1200 }
	cpu.ExecuteFrame(2000)
	cpu.BreakpointCheck = nil
	for cpu.Tstates() < 1600 {
		cpu.StepInstructionWithIRQ()
	}
	if cpu.nextFrameBoundary != 4100 {
		t.Errorf("step driver's next boundary %d, want 4100 (1500 + 2600)", cpu.nextFrameBoundary)
	}
	start := cpu.Clock() - cpu.Tstates()
	cpu.ExecuteFrame(2600)
	if got := cpu.Clock() - cpu.Tstates() - start; got != 4100 {
		t.Errorf("the frame run after stepping ended at %d, want 4100", got)
	}
}

// FrameSyncAt is captured with the CPU: the memory device restores the timing
// it derives from without re-running the observers that set it.
func TestFrameSyncAtIsCaptured(t *testing.T) {
	cpu := syncCPU(t)
	cpu.FrameSyncAt = 777
	b := cpu.SaveState()
	cpu.FrameSyncAt = 5
	if err := cpu.LoadState(b); err != nil {
		t.Fatal(err)
	}
	if cpu.FrameSyncAt != 777 {
		t.Errorf("FrameSyncAt %d after restore, want 777", cpu.FrameSyncAt)
	}
}
