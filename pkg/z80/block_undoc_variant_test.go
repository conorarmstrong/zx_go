package z80

import "testing"

// The Next's Z80N (T80 core) does NOT compute the undocumented F3/F5 flags of
// the block-load instructions (LDI/LDD/LDIR/LDDR) from n=A+value the way a
// real NMOS Z80 does — it preserves whatever F3/F5 were already in F. This was
// the dominant divergence behind NextBASIC's broken DEFPROC integer-parameter
// binding ("Integer out of range, 2550:1"): a stale NMOS F3/F5 rode a later
// PUSH AF into the bound parameter value. Real-Z80 models keep NMOS behaviour
// (zexall). Confirmed against CSpect (the Z80N reference): F3/F5 are constant
// across an LDIR regardless of the bytes transferred.

func TestLDIR_Z80N_PreservesF3F5(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.Variant = VariantZ80N
	cpu.PC = 0x8000
	cpu.A = 0x02
	cpu.F = FLAG_F3 | FLAG_Z // F3=1, F5=0 going in
	cpu.setHL(0x9000)
	cpu.setDE(0x9100)
	cpu.setBC(0x0001)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xB0)  // LDIR
	mem.Write(0x9000, 0x00)  // val=0 -> n=A=2; NMOS would give F3=bit3(2)=0, F5=bit1(2)=1
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_F3 == 0 {
		t.Errorf("Z80N LDIR: F3 cleared, want preserved (was 1)")
	}
	if cpu.F&FLAG_F5 != 0 {
		t.Errorf("Z80N LDIR: F5 set, want preserved (was 0)")
	}
}

func TestLDIR_Z80_ComputesF3F5FromN_Unchanged(t *testing.T) {
	cpu, mem := createTestCPU() // default VariantZ80 (real NMOS)
	cpu.PC = 0x8000
	cpu.A = 0x02
	cpu.F = FLAG_F3 | FLAG_Z
	cpu.setHL(0x9000)
	cpu.setDE(0x9100)
	cpu.setBC(0x0001)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xB0)
	mem.Write(0x9000, 0x00)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_F3 != 0 {
		t.Errorf("Z80 LDIR: F3 set, want clear (NMOS n=2 bit3=0)")
	}
	if cpu.F&FLAG_F5 == 0 {
		t.Errorf("Z80 LDIR: F5 clear, want set (NMOS n=2 bit1=1)")
	}
}
