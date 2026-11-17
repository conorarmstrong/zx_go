package z80

import "testing"

// Frames sit on a fixed grid. An instruction that runs past a frame's end
// spends part of the next frame: the next frame still starts at its nominal
// T-state, and its interrupt asserts there, inside that instruction, as on
// hardware. Starting each frame where the last instruction happened to end
// moved every interrupt onto an instruction boundary, where it was always
// sampled, so a long atomic prefix block could never miss the narrow pulse.
func TestNextFrameStartsOnTheGridNotAtTheOvershoot(t *testing.T) {
	m := &fiMem{b: make([]byte, 0x10000)}
	c := New(m, fiULA{})
	c.PC = 0x8000
	c.ExecuteFrame(1001) // NOPs (4 T) overshoot a 1001 T frame by 3
	if c.tstates == 0 {
		t.Fatal("precondition: no overshoot")
	}
	over := c.tstates
	c.ExecuteFrame(1001)
	if c.frameOrigin != 0 {
		t.Errorf("frame origin %d after an overshoot of %d, want 0 (the grid point)", c.frameOrigin, over)
	}
}

// A narrow pulse that falls entirely inside an instruction straddling the
// frame start is lost, as on hardware.
func TestPulseInsideAStraddlingInstructionIsLost(t *testing.T) {
	m := &fiMem{b: make([]byte, 0x10000)}
	c := New(m, fiULA{})
	c.IntAssertTstate, c.IntPulseTstates = 0, 32
	c.IFF1, c.IFF2, c.IM = true, true, 1
	c.PC = 0x8000
	c.tstates = 40 // the straddling instruction ended 40 T into this frame
	c.ExecuteFrame(1000)
	if c.PC < 0x8000 {
		t.Errorf("the interrupt was taken (PC=%04X); its pulse ended inside the straddling instruction", c.PC)
	}
}

// A DMA block that runs past the frame end holds the CPU off the bus while the
// next frame's interrupt asserts. Polling reaches it at its grid time, and the
// execution loop must not raise it a second time when the next frame starts.
func TestPolledInterruptPastTheFrameEndIsRaisedOnce(t *testing.T) {
	m := &fiMem{b: make([]byte, 0x10000)}
	c := New(m, fiULA{})
	c.IntAssertTstate, c.IntPulseTstates = 0, 32
	c.PC = 0x8000
	fired := 0
	c.SetFrameIntHook(func() { fired++ })
	c.ExecuteFrame(1000) // frame 0: its interrupt at 0
	base := fired
	// The CPU is held in a block that has reached 1010 T: past frame 0's end.
	c.PollIntSources(c.tstates + 1000 + 10 - c.tstates)
	if fired != base+1 {
		t.Fatalf("polling past the frame end fed %d interrupts, want 1 (frame 1's)", fired-base)
	}
	c.tstates += 1000 // the block's charge lands
	c.ExecuteFrame(1000)
	if fired != base+1 {
		t.Errorf("frame 1's interrupt was fed again by the loop (%d total, want 1)", fired-base)
	}
}
