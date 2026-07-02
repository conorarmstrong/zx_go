package z80

import "testing"

// WZ/MEMPTR updates for EX (SP),IX / EX (SP),IY.
//
// Per Sean Young §A.1: EX (SP),HL/IX/IY all set MEMPTR to the new
// value of the destination register (post-swap).

func TestEX_SP_IX_WZIsNewIX(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.SP = 0xC000
	cpu.IX = 0x1234
	cpu.WZ = 0xDEAD
	// Place a known word at (SP) so we know what the new IX will be.
	mem.Write(0xC000, 0x78)
	mem.Write(0xC001, 0x56)
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0xE3)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.IX != 0x5678 {
		t.Fatalf("setup wrong: IX after = $%04X, want $5678", cpu.IX)
	}
	if cpu.WZ != 0x5678 {
		t.Errorf("EX (SP),IX: WZ = $%04X, want $5678 (new IX)", cpu.WZ)
	}
}

func TestEX_SP_IY_WZIsNewIY(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.SP = 0xC000
	cpu.IY = 0xABCD
	cpu.WZ = 0xDEAD
	mem.Write(0xC000, 0x34)
	mem.Write(0xC001, 0x12)
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0xE3)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.IY != 0x1234 {
		t.Fatalf("setup wrong: IY after = $%04X, want $1234", cpu.IY)
	}
	if cpu.WZ != 0x1234 {
		t.Errorf("EX (SP),IY: WZ = $%04X, want $1234 (new IY)", cpu.WZ)
	}
}
