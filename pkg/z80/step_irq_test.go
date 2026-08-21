package z80

import "testing"

// StepInstructionWithIRQ asserts the frame interrupt at each frame
// boundary. The boundary was a hardcoded 70908 T, which is the Spectrum
// Next / 128K frame. A SAM Coupé frame is 119808 T with its INT at
// 99840, so single-stepping a SAM never latched the frame interrupt at
// all: HALT never ended and IM 1 handlers never ran.
func TestStepFrameBudgetFollowsTheMachine(t *testing.T) {
	c, _ := createTestCPU()
	if got := c.frameBudgetTstates(); got != 70908 {
		t.Errorf("default frame budget = %d, want 70908", got)
	}
	c.FrameTStates = 119808
	if got := c.frameBudgetTstates(); got != 119808 {
		t.Errorf("SAM frame budget = %d, want 119808", got)
	}
}

// Crossing a boundary places the next one a whole machine frame later,
// so the interrupt keeps arriving at 50 Hz on that machine rather than
// at the 128K rate.
func TestStepPlacesTheNextBoundaryAMachineFrameLater(t *testing.T) {
	c, _ := createTestCPU()
	c.FrameTStates = 119808
	c.SetTstates(119808)
	c.nextFrameBoundary = 119808
	c.IRQPending.Store(false)

	c.StepInstructionWithIRQ()

	if !c.IRQPending.Load() && c.PC == 0x0000 {
		t.Error("crossing the SAM frame boundary did not assert the frame interrupt")
	}
	if got := c.nextFrameBoundary; got != 2*119808 {
		t.Errorf("next boundary = %d, want %d (one SAM frame on)", got, 2*119808)
	}
}
