package z80

import "testing"

// The narrow frame-INT pulse is a fixed window [assertAt, assertAt+width).
// The CPU only latches the INT if an M1 sample point falls INSIDE that
// window. If a single long/atomic instruction (e.g. a chained DD/FD prefix
// block, which the model executes as one uninterruptible unit) advances
// tstates from before the window to past its end, no sample lands inside and
// the narrow /INT deasserts unsampled — the interrupt is LOST, not raised late
// at the next boundary. These tests pin frameIntPulse's window logic.
//
// Regression origin: the MrKWatkins int_skip conformance test (DD/FD prefix
// blocks must inhibit interrupt acceptance). Before the window bound, case 1
// raised the latch on any tstates >= assertAt with no upper bound, so an
// atomic instruction spanning the window produced a spurious late INT.

func newPulseCPU(t *testing.T) *CPU {
	t.Helper()
	cpu, _ := createTestCPU()
	t.Cleanup(func() { cleanupTestROMs("test_roms_z80") })
	cpu.IntAssertTstate = 100
	cpu.IntPulseTstates = 32 // window = [100, 132)
	cpu.frameIntFired = false
	cpu.frameIntDeasct = false
	cpu.IRQPending.Store(false)
	return cpu
}

func TestFrameIntPulse_SampleInsideWindow_Raises(t *testing.T) {
	cpu := newPulseCPU(t)
	cpu.tstates = 110 // inside [100, 132)
	cpu.frameIntPulse(0)
	if !cpu.IRQPending.Load() {
		t.Error("sample at t=110 inside the pulse window must raise IRQ")
	}
	if !cpu.frameIntFired {
		t.Error("frameIntFired must latch once raised")
	}
}

func TestFrameIntPulse_SampleAfterWindow_LostNotRaised(t *testing.T) {
	cpu := newPulseCPU(t)
	// An atomic instruction jumped tstates from < 100 straight to 400,
	// past the whole [100,132) window without any sample inside it.
	cpu.tstates = 400
	cpu.frameIntPulse(0)
	if cpu.IRQPending.Load() {
		t.Error("pulse window fully skipped by an atomic instruction: INT must be LOST, not raised late")
	}
	if !cpu.frameIntFired || !cpu.frameIntDeasct {
		t.Error("a skipped pulse must be marked spent (fired+deasserted) so it can't fire later this frame")
	}
}

func TestFrameIntPulse_RaisedThenElapsed_Withdrawn(t *testing.T) {
	cpu := newPulseCPU(t)
	cpu.tstates = 110
	cpu.frameIntPulse(0) // raises in-window
	if !cpu.IRQPending.Load() {
		t.Fatal("precondition: should be raised in-window")
	}
	// Window elapses with the INT still un-accepted (IFF1=0, say): withdraw.
	cpu.tstates = 200
	cpu.frameIntPulse(0)
	if cpu.IRQPending.Load() {
		t.Error("raised-but-unaccepted INT must be withdrawn once the window elapses")
	}
}
