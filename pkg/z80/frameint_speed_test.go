package z80

import "testing"

// The frame interrupt must arrive once per ULA frame whatever the CPU speed is,
// and must keep arriving across a speed change.
//
// StepInstructionWithIRQ scales its frame budget by SpeedMultiplier, so the
// budget changes the instant the guest writes NextReg $07. The boundary is then
// recomputed from the ABSOLUTE T-state counter:
//
//	c.nextFrameBoundary = ((c.tstates / frameBudget) + 1) * frameBudget
//
// which re-phases the whole frame grid onto multiples of the NEW budget. A
// boundary left behind by a spell at one speed is not a multiple of the other
// speed's budget, so the recompute can push the next boundary up to a full
// frame further out than the cadence the machine was keeping, and the frame
// interrupts that belonged in the gap are never raised.
//
// This was written to prove that re-phasing loses interrupts. It does not: the
// test passes, and the hypothesis is recorded here as refuted rather than
// deleted, because the invariant is worth keeping either way. The frame
// interrupt genuinely does survive a speed change, so anything that looks like a
// lost interrupt after one has a different cause.

type fiMem struct{ b []byte }

func (m *fiMem) Read(a uint16) byte     { return m.b[a] }
func (m *fiMem) Write(a uint16, v byte) { m.b[a] = v }
func (m *fiMem) ContendPort(uint16)     {}

type fiULA struct{}

func (fiULA) ReadPort(uint16) (byte, bool) { return 0xFF, true }
func (fiULA) WritePort(uint16, byte)       {}

// frameIntCPU is a machine parked on a one-byte NOP loop with the Next's narrow
// frame-interrupt pulse configured, so every accepted INT vectors to $0038.
func frameIntCPU() *CPU {
	m := &fiMem{b: make([]byte, 0x10000)}
	c := New(m, fiULA{})
	c.IntAssertTstate, c.IntPulseTstates = 291, 32
	c.FrameTStates = 70908
	c.IFF1, c.IFF2 = true, true
	c.IM = 1
	c.PC = 0x8000
	return c
}

// runFrameIntFrames steps until the T-state counter passes until, counting how
// many times the CPU vectored to the IM 1 handler.
func runFrameIntFrames(c *CPU, until uint64) int {
	taken := 0
	for c.tstates < until {
		c.StepInstructionWithIRQ()
		if c.PC == 0x0038 {
			taken++
			c.PC = 0x8000
			c.IFF1 = true
		}
	}
	return taken
}

// TestFrameIntSurvivesACPUSpeedChange is the control-and-subject pair. Both run
// for ten 28 MHz ULA frames and must take ten interrupts; the only difference is
// that the subject spent a few frames at 3.5 MHz first, which leaves
// nextFrameBoundary a multiple of 70908 rather than of 70908*8.
func TestFrameIntSurvivesACPUSpeedChange(t *testing.T) {
	const turboBudget = uint64(70908 * 8)

	// Control: 28 MHz from the start, so every boundary is already a
	// multiple of the 28 MHz budget and no re-phasing can happen.
	ctl := frameIntCPU()
	ctl.SetSpeedSelect(3)
	runFrameIntFrames(ctl, turboBudget) // settle onto a boundary
	ctlTaken := runFrameIntFrames(ctl, ctl.tstates+10*turboBudget)

	// Subject: three frames at 3.5 MHz, then up to 28 MHz.
	sub := frameIntCPU()
	sub.SetSpeedSelect(0)
	runFrameIntFrames(sub, 3*70908)
	if sub.nextFrameBoundary%turboBudget == 0 {
		t.Fatalf("fixture is not exercising the bug: boundary %d is already a "+
			"multiple of the 28 MHz budget, so no re-phasing can occur",
			sub.nextFrameBoundary)
	}
	sub.SetSpeedSelect(3)
	subTaken := runFrameIntFrames(sub, sub.tstates+10*turboBudget)

	if ctlTaken != subTaken {
		t.Errorf("frame INTs over ten 28 MHz frames: %d after a speed change, %d without one. "+
			"A speed change re-phased the frame grid and dropped %d interrupt(s)",
			subTaken, ctlTaken, ctlTaken-subTaken)
	}
}
