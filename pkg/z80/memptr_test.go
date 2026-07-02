package z80

import "testing"

// MEMPTR / WZ register tests. Per Sean Young §A.1, WZ
// is an undocumented internal register the Z80 uses to compose
// memory addresses. Its current value affects the F3/F5 flags of
// BIT n,(HL), so software depending on those bits sees different
// behaviour if WZ isn't tracked correctly.

func TestWZ_OUT_n_A(t *testing.T) {
	// OUT (n),A: WZ_low = (n+1) & $FF; WZ_high = A.
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.A = 0x42
	mem.Write(0x8000, 0xD3)
	mem.Write(0x8001, 0xFF) // n = $FF
	cpu.StepInstruction()
	// WZ_low = ($FF + 1) & $FF = $00; WZ_high = $42.
	want := uint16(0x4200)
	if cpu.WZ != want {
		t.Errorf("OUT (n=$FF),A=$42: WZ = $%04X, want $%04X", cpu.WZ, want)
	}
}

func TestWZ_OUT_n_A_NonWrappingN(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.A = 0x12
	mem.Write(0x8000, 0xD3)
	mem.Write(0x8001, 0x34) // n = $34
	cpu.StepInstruction()
	// WZ_low = $35; WZ_high = $12.
	want := uint16(0x1235)
	if cpu.WZ != want {
		t.Errorf("OUT (n=$34),A=$12: WZ = $%04X, want $%04X", cpu.WZ, want)
	}
}

func TestWZ_IN_A_n(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.A = 0x12
	mem.Write(0x8000, 0xDB)
	mem.Write(0x8001, 0x34) // n = $34
	cpu.StepInstruction()
	// WZ = (A<<8 | n) + 1 = $1234 + 1 = $1235.
	want := uint16(0x1235)
	if cpu.WZ != want {
		t.Errorf("IN A=$12,(n=$34): WZ = $%04X, want $%04X", cpu.WZ, want)
	}
}

func TestWZ_EX_SP_HL(t *testing.T) {
	// EX (SP),HL: WZ = new HL = old value at SP.
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0x9000
	cpu.H, cpu.L = 0xAA, 0xBB
	mem.Write(0x9000, 0xCD) // low byte of stacked value
	mem.Write(0x9001, 0xAB) // high byte
	mem.Write(0x8000, 0xE3) // EX (SP),HL
	cpu.StepInstruction()
	// After: HL = $ABCD; WZ = HL = $ABCD.
	if cpu.WZ != 0xABCD {
		t.Errorf("EX (SP),HL: WZ = $%04X, want $ABCD", cpu.WZ)
	}
}

func TestWZ_LD_A_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x9000, 0x42)
	mem.Write(0x8000, 0x3A) // LD A,(nn)
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x90) // nn = $9000
	cpu.StepInstruction()
	// WZ = nn + 1 = $9001.
	if cpu.WZ != 0x9001 {
		t.Errorf("LD A,($9000): WZ = $%04X, want $9001", cpu.WZ)
	}
}

func TestWZ_LD_nn_A(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.A = 0x55
	mem.Write(0x8000, 0x32) // LD (nn),A
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x90) // nn = $9000
	cpu.StepInstruction()
	// WZ_high = A = $55; WZ_low = (nn_low + 1) & $FF = ($00 + 1) = $01.
	want := uint16(0x5501)
	if cpu.WZ != want {
		t.Errorf("LD ($9000),A=$55: WZ = $%04X, want $%04X", cpu.WZ, want)
	}
}

// RST WZ updates — WZ = vector after each RST.

func TestWZ_RST(t *testing.T) {
	cases := []struct {
		op   byte
		want uint16
	}{
		{0xC7, 0x0000}, {0xCF, 0x0008},
		{0xD7, 0x0010}, {0xDF, 0x0018},
		{0xE7, 0x0020}, {0xEF, 0x0028},
		{0xF7, 0x0030}, {0xFF, 0x0038},
	}
	for _, c := range cases {
		cpu, mem := createTestCPU()
		cpu.PC = 0x8000
		cpu.SP = 0xFFF0
		mem.Write(0x8000, c.op)
		cpu.StepInstruction()
		if cpu.WZ != c.want {
			t.Errorf("RST $%02X: WZ = $%04X, want $%04X", c.op-0xC7, cpu.WZ, c.want)
		}
		cleanupTestROMs("test_roms_z80")
	}
}

// ED IN r,(C) / OUT (C),r → WZ = BC + 1.
func TestWZ_ED_IN_OUT_C(t *testing.T) {
	cases := []byte{
		0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x70, 0x78, // IN r,(C)
		0x41, 0x49, 0x51, 0x59, 0x61, 0x69, 0x71, 0x79, // OUT (C),r
	}
	for _, op := range cases {
		cpu, mem := createTestCPU()
		cpu.PC = 0x8000
		cpu.B = 0x12
		cpu.C = 0x34
		mem.Write(0x8000, 0xED)
		mem.Write(0x8001, op)
		cpu.StepInstruction()
		// BC = $1234 → WZ = $1235.
		if cpu.WZ != 0x1235 {
			t.Errorf("ED $%02X with BC=$1234: WZ = $%04X, want $1235", op, cpu.WZ)
		}
		cleanupTestROMs("test_roms_z80")
	}
}

// CALL/RET/JR/DJNZ WZ updates.

func TestWZ_CALL_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	mem.Write(0x8000, 0xCD) // CALL nn
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x90)
	cpu.StepInstruction()
	if cpu.WZ != 0x9000 {
		t.Errorf("CALL $9000: WZ = $%04X, want $9000", cpu.WZ)
	}
}

func TestWZ_CALL_cc_nn_SetsAlways(t *testing.T) {
	// CALL cc nn sets WZ even when NOT taken.
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.F = FLAG_Z          // for CALL NZ, this means NOT taken
	mem.Write(0x8000, 0xC4) // CALL NZ,nn
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x90)
	cpu.StepInstruction()
	if cpu.WZ != 0x9000 {
		t.Errorf("CALL NZ,$9000 not taken: WZ = $%04X, want $9000 (set regardless)", cpu.WZ)
	}
}

func TestWZ_RET(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0xAB) // low return PC
	mem.Write(0xFFF1, 0xCD) // high return PC
	mem.Write(0x8000, 0xC9) // RET
	cpu.StepInstruction()
	if cpu.WZ != 0xCDAB {
		t.Errorf("RET: WZ = $%04X, want $CDAB (popped PC)", cpu.WZ)
	}
}

func TestWZ_RET_cc_NotSetWhenNotTaken(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.WZ = 0x4242
	cpu.F = FLAG_Z // RET NZ NOT taken
	mem.Write(0xFFF0, 0xAB)
	mem.Write(0xFFF1, 0xCD)
	mem.Write(0x8000, 0xC0) // RET NZ
	cpu.StepInstruction()
	// Not taken — WZ unchanged.
	if cpu.WZ != 0x4242 {
		t.Errorf("RET NZ not taken: WZ = $%04X, want $4242 (unchanged)", cpu.WZ)
	}
}

func TestWZ_JR_TakenSetsTarget(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x18) // JR
	mem.Write(0x8001, 0x10) // +16 offset
	cpu.StepInstruction()
	// JR offset $10 → PC was $8000+2 = $8002, then +$10 = $8012.
	if cpu.WZ != 0x8012 {
		t.Errorf("JR +$10: WZ = $%04X, want $8012", cpu.WZ)
	}
}

func TestWZ_DJNZ_TakenSetsTarget(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.B = 5
	mem.Write(0x8000, 0x10) // DJNZ
	mem.Write(0x8001, 0xFE) // -2 offset → loops back
	cpu.StepInstruction()
	// B-- = 4 → taken; PC = $8000+2-2 = $8000.
	if cpu.WZ != 0x8000 {
		t.Errorf("DJNZ -2 taken: WZ = $%04X, want $8000", cpu.WZ)
	}
}

func TestWZ_DJNZ_NotTakenLeavesUnchanged(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.B = 1 // → 0 after DJNZ = not taken
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0x10)
	mem.Write(0x8001, 0xFE)
	cpu.StepInstruction()
	if cpu.WZ != 0xDEAD {
		t.Errorf("DJNZ not taken: WZ = $%04X, want $DEAD (unchanged)", cpu.WZ)
	}
}

func TestWZ_JP_nn_SetsToTarget(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xC3) // JP nn
	mem.Write(0x8001, 0x00)
	mem.Write(0x8002, 0x90) // target $9000
	cpu.StepInstruction()
	// WZ = target.
	if cpu.WZ != 0x9000 {
		t.Errorf("JP $9000: WZ = $%04X, want $9000", cpu.WZ)
	}
}
