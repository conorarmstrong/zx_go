package z80

import "testing"

// WZ/MEMPTR updates for ADD HL,rr / ADD IX,rr / ADD IY,rr.
//
// Per Sean Young §3.4, every 16-bit ADD sets MEMPTR = HL+1 (or
// IX+1 / IY+1), using the value of the destination register BEFORE
// the add. The ED-prefix ADC HL,rr / SBC HL,rr variants already
// handle this; the base ADD HL,rr and the DD/FD ADD IX/IY,rr did
// not.

func TestADD_HL_BC_WZIsHLPlus1(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.setHL(0x4000)
	cpu.setBC(0x0123)
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0x09) // ADD HL,BC
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4001 {
		t.Errorf("ADD HL,BC: WZ = $%04X, want $4001", cpu.WZ)
	}
}

func TestADD_HL_HL_WZIsHLPlus1(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.setHL(0x4000)
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0x29) // ADD HL,HL
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4001 {
		t.Errorf("ADD HL,HL: WZ = $%04X, want $4001", cpu.WZ)
	}
}

func TestADD_IX_BC_WZIsIXPlus1(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x5000
	cpu.setBC(0x0010)
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x09) // ADD IX,BC
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x5001 {
		t.Errorf("ADD IX,BC: WZ = $%04X, want $5001", cpu.WZ)
	}
}

func TestADD_IY_DE_WZIsIYPlus1(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IY = 0x6000
	cpu.setDE(0x0020)
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0x19) // ADD IY,DE
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x6001 {
		t.Errorf("ADD IY,DE: WZ = $%04X, want $6001", cpu.WZ)
	}
}
