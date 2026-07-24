package z80

import "testing"

// A HALTed Z80 keeps issuing M1 cycles for the HALT opcode, one every 4
// T-states, and every M1 bumps R (Sean Young §5 / Zilog datasheet). That is
// already asserted for ExecuteRZXFrame by halt_r_bump_test.go, but the
// emulator has four execution loops and they used to disagree:
//
//	ExecuteRZXFrame         +=4  bump
//	StepInstruction         +=4  no bump
//	StepInstructionWithIRQ  +=4  bump only when RefreshDuringHalt was set
//	ExecuteFrame            ++   no bump, RefreshDuringHalt ignored
//
// ExecuteFrame is the production loop for every Spectrum and Next model, so
// `LD A,R` after a HALT returned a frozen value there — stale entropy for the
// software that uses R as a PRNG seed, and a wrong read for anything that
// samples it. These lock all four loops to the one hardware behaviour.

// haltTicks is how many halted M1 cycles fit in n T-states.
func wantRAfter(start byte, ticks int) byte {
	r := start
	for i := 0; i < ticks; i++ {
		r = (r & 0x80) | ((r + 1) & 0x7F)
	}
	return r
}

func TestHALTRefreshInExecuteFrame(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x76) // HALT
	cpu.StepInstruction()   // enter HALT
	if !cpu.Halted {
		t.Fatal("CPU did not enter HALT")
	}
	cpu.IFF1 = false // no INT, so the frame runs out entirely halted
	rBefore := cpu.R

	// budget/4 halted M1 cycles fit in the frame. Asserting the bump count
	// pins both halves of the behaviour at once: that R advances at all, and
	// that a halted M1 costs 4 T-states rather than 1.
	const budget = 400
	cpu.ExecuteFrame(budget)

	if want := wantRAfter(rBefore, budget/4); cpu.R != want {
		t.Errorf("R after a halted frame = $%02X, want $%02X (one bump per 4 T-states)", cpu.R, want)
	}
}

func TestHALTRefreshInStepInstructionWithIRQ(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x76)
	cpu.StepInstruction()
	cpu.IFF1 = false
	rBefore := cpu.R

	const n = 10
	for i := 0; i < n; i++ {
		cpu.StepInstructionWithIRQ()
	}
	if want := wantRAfter(rBefore, n); cpu.R != want {
		t.Errorf("R after %d halted steps = $%02X, want $%02X", n, cpu.R, want)
	}
}

func TestHALTRefreshInStepInstruction(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x76)
	cpu.StepInstruction()
	rBefore := cpu.R

	const n = 10
	for i := 0; i < n; i++ {
		cpu.StepInstruction()
	}
	if want := wantRAfter(rBefore, n); cpu.R != want {
		t.Errorf("R after %d halted steps = $%02X, want $%02X", n, cpu.R, want)
	}
}
