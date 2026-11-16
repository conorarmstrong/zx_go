package z80

import "testing"

// PollIntSources places the frame and line INTs at a T-state ahead of the CPU
// clock, as while a DMA block holds the CPU off the bus.

func TestPollIntSourcesIsANoOpBeforeAFrame(t *testing.T) {
	cpu := newPulseCPU(t)
	fired := false
	cpu.SetFrameIntHook(func() { fired = true })
	cpu.PollIntSources(110)
	if fired || cpu.IRQPending.Load() {
		t.Error("poll acted before a frame was established")
	}
}

func TestPollIntSourcesFeedsTheFrameIntAhead(t *testing.T) {
	cpu := newPulseCPU(t)
	cpu.frameOrigin, cpu.frameOriginValid = 0, true
	cpu.tstates = 50 // the CPU clock is still before the pulse
	fired := 0
	cpu.SetFrameIntHook(func() { fired++ })
	cpu.PollIntSources(90)
	if fired != 0 {
		t.Fatal("frame INT fed before its assert point")
	}
	cpu.PollIntSources(110)
	if fired != 1 || !cpu.IRQPending.Load() {
		t.Errorf("frame INT at 110: hook %d, pending %v", fired, cpu.IRQPending.Load())
	}
	cpu.PollIntSources(120)
	if fired != 1 {
		t.Error("frame INT fed twice in one frame")
	}
	// The block ends past the window: the unsampled pulse is withdrawn.
	cpu.PollIntSources(140)
	if cpu.IRQPending.Load() {
		t.Error("pulse still pending after its window")
	}
}

func TestPollIntSourcesFeedsTheLineInt(t *testing.T) {
	cpu := newPulseCPU(t)
	cpu.frameOrigin, cpu.frameOriginValid = 1000, true
	cpu.LineIntOffsetTstates = 500
	cpu.lineIntFired = false
	fired := 0
	cpu.SetLineIntHook(func() { fired++ })
	cpu.PollIntSources(1499)
	if fired != 0 {
		t.Fatal("line INT before its line")
	}
	cpu.PollIntSources(1500)
	cpu.PollIntSources(1600)
	if fired != 1 {
		t.Errorf("line INT fired %d times, want 1", fired)
	}
}

func TestResetInvalidatesTheFrameOrigin(t *testing.T) {
	cpu := newPulseCPU(t)
	cpu.frameOrigin, cpu.frameOriginValid = 0, true
	cpu.Reset()
	fired := false
	cpu.SetFrameIntHook(func() { fired = true })
	cpu.PollIntSources(110)
	if fired {
		t.Error("poll used a frame origin from before the reset")
	}
}
