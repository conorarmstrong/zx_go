package z80

import "testing"

// The Next's Z80N leaves H untouched (preserves it) after CCF, whereas a real
// NMOS Z80 sets H to the OLD carry. (CSpect-verified: post-CCF H equals the
// pre-CCF H regardless of the carry.) One of the residual undocumented-flag
// divergences exposed while fixing NextBASIC's DEFPROC integer-parameter
// binding. Real-Z80 models keep NMOS behaviour (zexall).

func TestCCF_Z80N_PreservesH(t *testing.T) {
	// H set going in, carry clear: NMOS would set H = old C = 0; Z80N keeps H.
	cpu, mem := createTestCPU()
	cpu.Variant = VariantZ80N
	cpu.PC = 0x8000
	cpu.A = 0x00
	cpu.F = FLAG_H
	mem.Write(0x8000, 0x3F) // CCF
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_H == 0 {
		t.Errorf("Z80N CCF: H cleared, want preserved (was set)")
	}

	// H clear going in, carry set: NMOS would set H = old C = 1; Z80N keeps H=0.
	cpu2, mem2 := createTestCPU()
	cpu2.Variant = VariantZ80N
	cpu2.PC = 0x8000
	cpu2.A = 0x00
	cpu2.F = FLAG_C
	mem2.Write(0x8000, 0x3F)
	cpu2.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu2.F&FLAG_H != 0 {
		t.Errorf("Z80N CCF: H set, want preserved clear (NMOS would set H = old carry)")
	}
}

func TestCCF_Z80_HFromOldCarry_Unchanged(t *testing.T) {
	cpu, mem := createTestCPU() // default VariantZ80
	cpu.PC = 0x8000
	cpu.A = 0x00
	cpu.F = FLAG_C // old carry set -> NMOS H = old C = 1
	mem.Write(0x8000, 0x3F)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_H == 0 {
		t.Errorf("Z80 CCF: H clear, want set (NMOS H = old carry)")
	}
}
