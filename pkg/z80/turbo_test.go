package z80

import "testing"

// TestTurboFrameBudgetScales pins the contract that ExecuteFrame
// consumes tstatesPerFrame * SpeedMultiplier() CPU T-states per
// call. The CPU's tstates counter is reset to its residue at the
// end of each frame, so we measure via InstructionCount (which is
// monotonic). NOPs are 4 T-states each, so the count scales with
// the multiplier.
func TestTurboFrameBudgetScales(t *testing.T) {
	cases := []struct {
		name string
		sel  byte
		mult uint64
	}{
		{"3.5 MHz", 0, 1},
		{"7 MHz", 1, 2},
		{"14 MHz", 2, 4},
		{"28 MHz", 3, 8},
	}

	// Place an infinite "JR $" (jump-to-self, opcode 18 FE) in
	// RAM. This pins PC in zeroed RAM (every fetch is exactly
	// the JR + its offset byte), so the count is purely a
	// function of T-state budget and per-instruction cost (12
	// T-states for JR with no extra wait, 13 at 28 MHz).
	var baseline uint64
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cpu, mem := createTestCPU()
			cpu.Variant = VariantZ80N
			cpu.SetSpeedSelect(c.sel)
			cpu.IFF1 = false
			cpu.PC = 0x4000
			mem.Write(0x4000, 0x18) // JR
			mem.Write(0x4001, 0xFE) // -2 -> back to 0x4000
			start := cpu.InstructionCount()
			cpu.ExecuteFrame(69888)
			ran := cpu.InstructionCount() - start
			if ran == 0 {
				t.Fatalf("%s: zero instructions executed", c.name)
			}
			if c.sel == 0 {
				baseline = ran
				return
			}
			want := baseline * c.mult
			// 28 MHz pays a +1 T-state per M1 fetch (JR has one
			// M1 fetch), so each JR iteration costs 13 not 12
			// T-states. That makes 28 MHz close to 8 × 12/13 =
			// 7.4× baseline. 15% tolerance comfortably covers
			// that plus minor loop-edge effects.
			lo := want * 85 / 100
			hi := want * 115 / 100
			if ran < lo || ran > hi {
				t.Errorf("%s: ran %d instructions, want ~%d (range %d..%d)", c.name, ran, want, lo, hi)
			}
		})
	}
}

// TestTurbo28MHzM1WaitState pins the M1-wait-state contract: at
// 28 MHz on Variant==VariantZ80N, every M1 fetch costs one extra
// CPU T-state. We measure by running a single NOP at each speed
// and comparing the deltas.
func TestTurbo28MHzM1WaitState(t *testing.T) {
	tstatesPerNOPNo28 := func(t *testing.T, sel byte, variant Variant) uint64 {
		t.Helper()
		cpu, mem := createTestCPU()
		cpu.Variant = variant
		cpu.SetSpeedSelect(sel)
		cpu.PC = 0x4000
		mem.Write(0x4000, 0x00) // NOP
		startT := cpu.Tstates()
		cpu.executeInstruction()
		return cpu.Tstates() - startT
	}

	base := tstatesPerNOPNo28(t, 0, VariantZ80)      // 3.5 MHz Z80
	z80At28 := tstatesPerNOPNo28(t, 3, VariantZ80)   // 28 MHz Z80 (no wait — variant gates)
	z80nAt7 := tstatesPerNOPNo28(t, 1, VariantZ80N)  // 7 MHz Z80N (no wait — speed gates)
	z80nAt28 := tstatesPerNOPNo28(t, 3, VariantZ80N) // 28 MHz Z80N (wait active)

	if base != 4 {
		t.Errorf("baseline NOP at 3.5 MHz Z80 = %d T-states, want 4", base)
	}
	if z80At28 != 4 {
		t.Errorf("28 MHz Z80 NOP = %d, want 4 (wait state should NOT trigger on plain Z80)", z80At28)
	}
	if z80nAt7 != 4 {
		t.Errorf("7 MHz Z80N NOP = %d, want 4 (wait state only at 28 MHz)", z80nAt7)
	}
	if z80nAt28 != 5 {
		t.Errorf("28 MHz Z80N NOP = %d, want 5 (M1 wait state adds 1)", z80nAt28)
	}
}

// TestExistingModelsUnchangedAtDefaultSpeed locks in the
// regression baseline: an out-of-the-box Z80 CPU with
// SpeedSelect=0 must run exactly the documented T-states for a
// given instruction. This catches accidental wiring changes that
// would alter timing for non-Next models.
func TestExistingModelsUnchangedAtDefaultSpeed(t *testing.T) {
	cpu, mem := createTestCPU()
	// Variant defaults to VariantZ80, speedSelect=0.
	cpu.PC = 0x4000
	mem.Write(0x4000, 0x3E) // LD A, n
	mem.Write(0x4001, 0x42) // n = 0x42
	startT := cpu.Tstates()
	cpu.executeInstruction()
	if got := cpu.Tstates() - startT; got != 7 {
		t.Errorf("LD A,n at 3.5 MHz Z80 = %d T-states, want 7", got)
	}
	if cpu.A != 0x42 {
		t.Errorf("LD A,n result: A = %#x, want 0x42", cpu.A)
	}
}
