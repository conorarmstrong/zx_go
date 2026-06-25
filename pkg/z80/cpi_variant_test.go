package z80

import "testing"

// The Next's Z80N takes the undocumented F3/F5 flags of the block-compare
// instructions (CPI/CPD/CPIR/CPDR) from the operand byte read from (HL) —
// like a normal CP — rather than from NMOS's n = A-(HL)-H result byte. This
// was the final undocumented-flag divergence behind NextBASIC's broken
// DEFPROC integer-parameter binding. (CSpect-verified: A=$0D, (HL)=$AC ->
// F3/F5 = bits 3,5 of $AC = both set, whereas NMOS n=$61 gives both clear.)
// Real-Z80 models keep NMOS behaviour (zexall).

func TestCPI_Z80N_UndocFromOperand(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.Variant = VariantZ80N
	cpu.PC = 0x8000
	cpu.A = 0x0D
	cpu.setHL(0x9000)
	cpu.setBC(0x0002)
	cpu.F = 0
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA1) // CPI
	mem.Write(0x9000, 0xAC) // operand: bit3=1, bit5=1
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_F3 == 0 {
		t.Errorf("Z80N CPI: F3 clear, want set (operand $AC bit3=1)")
	}
	if cpu.F&FLAG_F5 == 0 {
		t.Errorf("Z80N CPI: F5 clear, want set (operand $AC bit5=1)")
	}
}

func TestCPI_Z80_UndocFromResult_Unchanged(t *testing.T) {
	cpu, mem := createTestCPU() // default VariantZ80 (real NMOS)
	cpu.PC = 0x8000
	cpu.A = 0x0D
	cpu.setHL(0x9000)
	cpu.setBC(0x0002)
	cpu.F = 0
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA1)
	mem.Write(0x9000, 0xAC)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	// NMOS: n = A-(HL)-H = $0D-$AC = $61 (H=0) -> F3=bit3($61)=0, F5=bit1($61)=0.
	if cpu.F&FLAG_F3 != 0 {
		t.Errorf("Z80 CPI: F3 set, want clear (NMOS n=$61 bit3=0)")
	}
	if cpu.F&FLAG_F5 != 0 {
		t.Errorf("Z80 CPI: F5 set, want clear (NMOS n=$61 bit1=0)")
	}
}
