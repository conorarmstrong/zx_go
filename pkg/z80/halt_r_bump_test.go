package z80

import "testing"

// HALT state R-register bump.
//
// Per Sean Young §5 / Zilog datasheet: while the CPU is HALTed it
// continuously issues M1 cycles for the HALT opcode internally,
// each of which bumps R. Without this a HALT loop would leave R
// frozen, so software using R as a PRNG seed (e.g. ZX 48K
// keyboard scan after a HALT) would see stale entropy.

func TestHALT_BumpsRPerInstruction(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x76) // HALT
	cpu.StepInstruction()
	rAfterHalt := cpu.R
	// Now step the CPU N more "instructions" — each should bump R.
	// Use ExecuteRZXFrame which counts halt-tick iterations as
	// instructions for RZX timing.
	const N = 10
	cpu.IFF1 = false // disable INT so the run ends without firing
	cpu.ExecuteRZXFrame(uint64(N))
	cleanupTestROMs("test_roms_z80")
	if !cpu.Halted {
		t.Fatal("CPU should still be Halted after no INT")
	}
	wantR := rAfterHalt
	for i := 0; i < N; i++ {
		wantR = (wantR & 0x80) | ((wantR + 1) & 0x7F)
	}
	if cpu.R != wantR {
		t.Errorf("HALT R bump after %d ticks: R = $%02X, want $%02X", N, cpu.R, wantR)
	}
}
