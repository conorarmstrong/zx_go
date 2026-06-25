package z80

import "testing"

// The Spectrum Next's Z80N (T80-derived core) takes the undocumented F3/F5
// flags of BIT n,(mem) from the VALUE byte that was read, not from the
// MEMPTR / address-high byte the way a real NMOS Z80 does. NextBASIC's
// DEFPROC integer-parameter binding relies on this: a BIT n,(IY+d) against a
// system-variable byte feeds its F3/F5 into a later PUSH AF, and the NMOS
// rule corrupts the bound value (observed as "Integer out of range, 2550:1"
// in NextBASIC Invaders). Real-Z80 models keep the NMOS behaviour (zexall).

func TestFDCB_BIT_Z80N_UndocFromValue(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.Variant = VariantZ80N
	cpu.PC = 0x8000
	cpu.IY = 0x2800 // addr-high $28 = 0010_1000 -> bit3=1, bit5=1
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0xCB)
	mem.Write(0x8002, 0x00) // d = 0 -> effective addr $2800
	mem.Write(0x8003, 0x5E) // BIT 3,(IY+0)
	mem.Write(0x2800, 0x00) // value byte: bit3=0, bit5=0
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_F3 != 0 {
		t.Errorf("Z80N BIT 3,(IY+0): F3 set, want 0 (from value byte $00, not addr-high $28)")
	}
	if cpu.F&FLAG_F5 != 0 {
		t.Errorf("Z80N BIT 3,(IY+0): F5 set, want 0 (from value byte $00)")
	}
}

func TestFDCB_BIT_Z80_UndocFromAddrHigh_Unchanged(t *testing.T) {
	cpu, mem := createTestCPU() // default VariantZ80 (real NMOS)
	cpu.PC = 0x8000
	cpu.IY = 0x2800
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0xCB)
	mem.Write(0x8002, 0x00)
	mem.Write(0x8003, 0x5E) // BIT 3,(IY+0)
	mem.Write(0x2800, 0x00)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.F&FLAG_F3 == 0 {
		t.Errorf("Z80 BIT 3,(IY+0): F3 clear, want set (addr-high $28 bit3=1)")
	}
	if cpu.F&FLAG_F5 == 0 {
		t.Errorf("Z80 BIT 3,(IY+0): F5 clear, want set (addr-high $28 bit5=1)")
	}
}
