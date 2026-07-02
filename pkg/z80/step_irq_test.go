package z80

import "testing"

// TestStepInstruction_HaltedBurnsTstatesOnly verifies the c.Halted
// fast path: while halted, StepInstruction burns 4 T-states without
// touching PC or executing anything.
func TestStepInstruction_HaltedBurnsTstatesOnly(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.Halted = true
	cpu.PC = 0xDEAD
	before := cpu.tstates
	cpu.StepInstruction()
	if cpu.PC != 0xDEAD {
		t.Errorf("StepInstruction while halted advanced PC to $%04X", cpu.PC)
	}
	if cpu.tstates-before != 4 {
		t.Errorf("StepInstruction halted: t-delta = %d, want 4", cpu.tstates-before)
	}
}

// TestStepInstructionWithIRQ_FiresAtFrameBoundary is the iter-124
// tracer bullet: stepping when the CPU's t-state count has reached
// the next frame boundary must assert IRQPending — without this,
// debugger-driven single-stepping never delivers the ULA INT and
// lockstep against another emulator diverges within ~10K
// instructions of compounded IRQ-acceptance differences.
//
// Verifies one behavior: tstates >= nextFrameBoundary on entry ⇒
// IRQPending becomes true after the step, and nextFrameBoundary
// advances to the next budget.
func TestStepInstructionWithIRQ_FiresAtFrameBoundary(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Position the CPU exactly at a frame boundary. Keep IFF1=false
	// so we observe IRQPending without it being accepted+cleared by
	// interrupt() inside the same step.
	cpu.tstates = stepFrameBudget
	cpu.nextFrameBoundary = stepFrameBudget
	cpu.IFF1 = false
	cpu.IRQPending.Store(false)

	cpu.StepInstructionWithIRQ()

	if !cpu.IRQPending.Load() {
		t.Errorf("IRQPending = false after step at frame boundary; want true")
	}
	if cpu.nextFrameBoundary != 2*stepFrameBudget {
		t.Errorf("nextFrameBoundary = %d, want %d (= 2 × budget)", cpu.nextFrameBoundary, 2*stepFrameBudget)
	}
}

// TestStepInstructionWithIRQ_FrameIntDisabledSuppresses verifies
// that NR$22 bit 1 (= FrameIntDisabled) suppresses the frame-
// boundary IRQ assertion. Software that disables the frame INT
// via NR$22 must continue to not see one even under debugger
// single-stepping. The boundary bookkeeping still advances; only
// the IRQPending side-effect is gated.
func TestStepInstructionWithIRQ_FrameIntDisabledSuppresses(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.tstates = stepFrameBudget
	cpu.nextFrameBoundary = stepFrameBudget
	cpu.IFF1 = false
	cpu.IRQPending.Store(false)
	cpu.FrameIntDisabled = true

	cpu.StepInstructionWithIRQ()

	if cpu.IRQPending.Load() {
		t.Errorf("IRQPending = true with FrameIntDisabled; want false")
	}
	if cpu.nextFrameBoundary != 2*stepFrameBudget {
		t.Errorf("nextFrameBoundary = %d, want %d (advances regardless of disable)", cpu.nextFrameBoundary, 2*stepFrameBudget)
	}
}

// TestStepInstructionWithIRQ_AcceptsPendingIRQ verifies the M1
// sample point: when IRQPending is set and IFF1 is enabled (and
// EI's one-instruction delay has expired), a single step delivers
// the interrupt — pushing PC and jumping to $0038 under IM 1, the
// mode NextZXOS uses. Without this the debugger's "step" command
// silently held interrupts off and lockstep against another
// emulator diverged within ~10K instructions of accumulated
// missed IRQs.
func TestStepInstructionWithIRQ_AcceptsPendingIRQ(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.PC = 0x1234
	cpu.SP = 0xFFFE
	cpu.IM = 1
	cpu.IFF1 = true
	cpu.eiDelay = false
	cpu.IRQPending.Store(true)
	// Park the boundary far in the future so the boundary path
	// doesn't muddy the assertion.
	cpu.tstates = 1000
	cpu.nextFrameBoundary = stepFrameBudget

	cpu.StepInstructionWithIRQ()

	// Step semantics: a single call services the IRQ (push + jump to
	// $0038 in IM 1) AND executes one instruction at the ISR entry —
	// matching ExecuteFrame's inner-loop body, which is the cadence
	// lockstep relies on. PC therefore lands somewhere in the
	// stand-in test ROM at/past $0038, not exactly $0038.
	if cpu.PC == 0x1234 {
		t.Errorf("PC unchanged at 0x1234 after step with pending IRQ; want PC inside the ISR")
	}
	if cpu.IFF1 {
		t.Errorf("IFF1 still set after IRQ accept; want cleared")
	}
	if cpu.IRQPending.Load() {
		t.Errorf("IRQPending still set after IRQ accept; want cleared")
	}
	if cpu.SP != 0xFFFC {
		t.Errorf("SP = %#04x after push; want 0xFFFC (-2 from 0xFFFE)", cpu.SP)
	}
	// Verify the pushed return address is the original PC, so RETI
	// resumes the interrupted code at the right place.
	low, high := cpu.mem.Read(0xFFFC), cpu.mem.Read(0xFFFD)
	if pushed := uint16(high)<<8 | uint16(low); pushed != 0x1234 {
		t.Errorf("pushed return address = %#04x, want 0x1234", pushed)
	}
}

// TestStepInstructionWithIRQ_EIDelayGates verifies Z80 EI's
// one-instruction grace period: after EI sets eiDelay=true, the
// next instruction must complete before any pending IRQ can be
// accepted. Without this, EI followed by RETI / RET in an ISR
// would immediately re-enter the ISR on the same pending edge.
//
// Two-step exercise: step 1 with eiDelay=true must NOT accept the
// IRQ; step 2 (eiDelay now cleared) must.
func TestStepInstructionWithIRQ_EIDelayGates(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.PC = 0x1234
	cpu.SP = 0xFFFE
	cpu.IM = 1
	cpu.IFF1 = true
	cpu.eiDelay = true
	cpu.IRQPending.Store(true)
	cpu.tstates = 1000
	cpu.nextFrameBoundary = stepFrameBudget

	// Step 1: IRQ should be HELD OFF because eiDelay was true at
	// the M1 sample point.
	cpu.StepInstructionWithIRQ()

	if !cpu.IRQPending.Load() {
		t.Errorf("step 1: IRQPending cleared while eiDelay was set; want still pending")
	}
	if !cpu.IFF1 {
		t.Errorf("step 1: IFF1 cleared (= IRQ was accepted) while eiDelay was set; want still set")
	}
	if cpu.eiDelay {
		t.Errorf("step 1: eiDelay still set after one step; want cleared")
	}

	// Step 2: eiDelay is now cleared, so the still-pending IRQ
	// must be accepted.
	cpu.StepInstructionWithIRQ()

	if cpu.IRQPending.Load() {
		t.Errorf("step 2: IRQPending still set; expected IRQ acceptance to clear it")
	}
	if cpu.IFF1 {
		t.Errorf("step 2: IFF1 still set; expected IRQ acceptance to clear it")
	}
}

// TestStepInstructionWithIRQ_HaltWakeOnInt verifies the Spectrum
// Next "HALT wakes on edge regardless of IFF1" path. With IFF1=0,
// the standard interrupt() path can't fire, but HaltWakeOnInt=true
// lets a pending IRQ still wake the halted CPU. Used by NextZXOS
// to busy-wait HALT for a frame edge without enabling interrupts.
//
// Critical detail: when HaltWakeOnInt fires, the CPU just exits
// HALT — it does NOT invoke interrupt(), so PC stays at the HALT
// site (the next step proceeds with the post-HALT instruction).
func TestStepInstructionWithIRQ_HaltWakeOnInt(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.PC = 0x1234
	cpu.SP = 0xFFFE
	cpu.IFF1 = false
	cpu.eiDelay = false
	cpu.Halted = true
	cpu.HaltWakeOnInt = true
	cpu.IRQPending.Store(true)
	cpu.tstates = 1000
	cpu.nextFrameBoundary = stepFrameBudget

	cpu.StepInstructionWithIRQ()

	if cpu.Halted {
		t.Errorf("Halted still true after HaltWakeOnInt step; want false")
	}
	if cpu.IRQPending.Load() {
		t.Errorf("IRQPending still set after wake; want cleared")
	}
	// PC must NOT have moved — wake-on-INT exits HALT without
	// dispatching the IRQ vector. SP must NOT have decremented
	// because no push happened.
	if cpu.PC != 0x1234 {
		t.Errorf("PC = %#04x; want 0x1234 (wake-on-INT does not invoke interrupt())", cpu.PC)
	}
	if cpu.SP != 0xFFFE {
		t.Errorf("SP = %#04x; want 0xFFFE (no push on wake-on-INT)", cpu.SP)
	}
}
