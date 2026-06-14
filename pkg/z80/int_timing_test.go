package z80

import "testing"

// Frame-INT timing conformance (timing.md §1a/§1c). The FPGA asserts the
// maskable frame interrupt as a NARROW pulse (32 CPU cycles 48K/+3, 36
// 128K/Pentagon — zxnext.vhd:2014-2033) at a specific point in the ULA
// frame (zxula_timing.vhd:551), NOT "held the whole frame". These tests
// pin that contract so the whole class of INT-timing divergences is fixed
// and locked together rather than discovered one boot-symptom at a time.

// TestFrameInt_NarrowPulse_MissedWhenDIAcrossPulse — matrix row B3.
// A `DI` that covers the entire pulse window must MISS the interrupt: an
// `EI` issued AFTER the pulse has withdrawn does not take a stale INT.
// Legacy "hold the whole frame" wrongly delivers the stale INT.
func TestFrameInt_NarrowPulse_MissedWhenDIAcrossPulse(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IM = 1
	cpu.IFF1 = false // interrupts disabled across the whole pulse window
	cpu.IFF2 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	// Narrow pulse: assert at tstate 20, width 32 → live during [20,52).
	cpu.IntAssertTstate = 20
	cpu.IntPulseTstates = 32

	// NOPs, with EI at $8014 (executes ~tstate 80, well AFTER the pulse
	// window) then more NOPs. So IFF1=0 throughout [20,52) and only goes
	// 1 long after the pulse has withdrawn.
	for a := 0x8000; a < 0x8100; a++ {
		mem.Write(uint16(a), 0x00) // NOP
	}
	mem.Write(0x8014, 0xFB) // EI

	sawInt := false
	cpu.BreakpointCheck = func(pc uint16) bool {
		if pc == 0x0038 { // IM1 vector — only reachable via INT acceptance
			sawInt = true
		}
		return false
	}

	cpu.ExecuteFrame(2000)

	if sawInt {
		t.Errorf("frame INT taken after the pulse window — narrow pulse not honoured " +
			"(stale held INT). Want: missed (DI covered the whole pulse).")
	}
}

// TestFrameInt_NarrowPulse_TakenWhenEnabledDuringPulse — matrix row B4.
// If interrupts are enabled while the pulse is live, the INT IS taken
// exactly once. Guards against the narrow-pulse fix over-correcting into
// "never fires".
func TestFrameInt_NarrowPulse_TakenWhenEnabledDuringPulse(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IM = 1
	cpu.IFF1 = true // enabled before the pulse
	cpu.IFF2 = true
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	// Assert later so several NOPs run with IFF1=1 before the pulse, and
	// the pulse is wide enough to be sampled while enabled.
	cpu.IntAssertTstate = 40
	cpu.IntPulseTstates = 32 // live [40,72)

	for a := 0x8000; a < 0x8100; a++ {
		mem.Write(uint16(a), 0x00) // NOP
	}

	intCount := 0
	cpu.BreakpointCheck = func(pc uint16) bool {
		if pc == 0x0038 {
			intCount++
		}
		return false
	}

	cpu.ExecuteFrame(2000)

	if intCount != 1 {
		t.Errorf("frame INT accepted %d times, want exactly 1 (enabled during a live pulse)", intCount)
	}
}

// TestStepIRQ_FrameBoundary_ScalesWithCPUSpeed — the single-step IRQ path
// (StepInstructionWithIRQ: debugger step, bisection, memdiff) must fire the
// frame interrupt once per ULA FRAME, not once per 70908 CPU T-states. At
// 28 MHz (NR$07=3, SpeedMultiplier 8) a ULA frame is 70908*8 CPU T-states,
// so a fixed-70908 boundary fires the INT 8x too often — exactly the
// spurious-interrupt artifact that derailed the Next cold-boot diagnostics.
// ExecuteFrame already scales its budget by SpeedMultiplier; this pins the
// same contract for the step path.
func TestStepIRQ_FrameBoundary_ScalesWithCPUSpeed(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IM = 1
	cpu.IFF1 = false // never accept — we only count INT-line assertions
	cpu.PC = 0x8000
	cpu.SetSpeedSelect(3) // 28 MHz → SpeedMultiplier 8

	// Narrow pulse so the INT line is raised AND withdrawn each frame,
	// giving a countable rising edge per frame (the held model would latch
	// once and never fall, so edges couldn't be counted).
	cpu.IntAssertTstate = 0
	cpu.IntPulseTstates = 32

	// JR -2 self-loop at $8000 so PC never wanders into unmapped memory.
	mem.Write(0x8000, 0x18)
	mem.Write(0x8001, 0xFE)

	const ulaFrame = 70908
	const frames = 4
	window := uint64(ulaFrame) * uint64(cpu.SpeedMultiplier()) * frames

	edges := 0
	prev := false
	for cpu.Tstates() < window {
		cpu.StepInstructionWithIRQ()
		cur := cpu.IRQPending.Load()
		if cur && !prev {
			edges++
		}
		prev = cur
	}

	// One frame INT per ULA frame over `frames` frames. A fixed-70908
	// boundary at SpeedMultiplier 8 would produce ~8x as many (≈32).
	if edges < frames-1 || edges > frames+1 {
		t.Errorf("frame-INT assertions = %d over %d ULA frames at 28 MHz, want ≈%d "+
			"(boundary not scaled by SpeedMultiplier → fires per 70908 CPU T-states)",
			edges, frames, frames)
	}
}
