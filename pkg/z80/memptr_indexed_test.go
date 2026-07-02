package z80

import "testing"

// WZ/MEMPTR updates for indexed addressing.
//
// Per Sean Young §3.4: EVERY instruction that uses (IX+d) or
// (IY+d) addressing sets MEMPTR = IX/IY + d. This covers:
//
//   - LD r,(IX+d) / LD r,(IY+d)
//   - LD (IX+d),r / LD (IY+d),r
//   - LD (IX+d),n / LD (IY+d),n
//   - INC/DEC (IX+d) / INC/DEC (IY+d)
//   - ADD/ADC/SUB/SBC/AND/OR/XOR/CP A,(IX+d) / (IY+d)
//
// (DDCB / FDCB are covered separately.)
//
// loadIXd/storeIXd/loadIYd/storeIYd update MEMPTR; without it a
// subsequent BIT n,(HL) would read stale F3/F5.

func TestLD_r_IXd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.WZ = 0xDEAD
	mem.Write(0x4010, 0x55)
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x46) // LD B,(IX+d)
	mem.Write(0x8002, 0x10) // d = $10
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4010 {
		t.Errorf("LD B,(IX+$10): WZ = $%04X, want $4010", cpu.WZ)
	}
}

func TestLD_IXd_r_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.B = 0x42
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x70) // LD (IX+d),B
	mem.Write(0x8002, 0x20) // d = $20
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4020 {
		t.Errorf("LD (IX+$20),B: WZ = $%04X, want $4020", cpu.WZ)
	}
}

func TestLD_r_IYd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IY = 0x5000
	cpu.WZ = 0xDEAD
	mem.Write(0x5008, 0x66)
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0x4E) // LD C,(IY+d)
	mem.Write(0x8002, 0x08)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x5008 {
		t.Errorf("LD C,(IY+$08): WZ = $%04X, want $5008", cpu.WZ)
	}
}

func TestLD_IYd_r_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IY = 0x5000
	cpu.A = 0x33
	cpu.WZ = 0xDEAD
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0x77) // LD (IY+d),A
	mem.Write(0x8002, 0x04)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x5004 {
		t.Errorf("LD (IY+$04),A: WZ = $%04X, want $5004", cpu.WZ)
	}
}

func TestINC_IXd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.WZ = 0xDEAD
	mem.Write(0x4030, 0x10)
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x34) // INC (IX+d)
	mem.Write(0x8002, 0x30)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4030 {
		t.Errorf("INC (IX+$30): WZ = $%04X, want $4030", cpu.WZ)
	}
}

func TestNegativeDisplacement_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.WZ = 0xDEAD
	mem.Write(0x3FFE, 0x99) // IX-2 = $3FFE
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x46) // LD B,(IX+d)
	mem.Write(0x8002, 0xFE) // d = -2
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x3FFE {
		t.Errorf("LD B,(IX+-2): WZ = $%04X, want $3FFE", cpu.WZ)
	}
}

// Confirm ALU ops on (IX+d) also pick up the WZ update (they all
// route through loadIXd).

func TestADD_A_IXd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.A = 0x10
	cpu.WZ = 0xDEAD
	mem.Write(0x4005, 0x20)
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x86) // ADD A,(IX+d)
	mem.Write(0x8002, 0x05)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4005 {
		t.Errorf("ADD A,(IX+$05): WZ = $%04X, want $4005", cpu.WZ)
	}
}

func TestCP_IXd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = 0x4000
	cpu.A = 0x42
	cpu.WZ = 0xDEAD
	mem.Write(0x4007, 0x42)
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0xBE) // CP (IX+d)
	mem.Write(0x8002, 0x07)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x4007 {
		t.Errorf("CP (IX+$07): WZ = $%04X, want $4007", cpu.WZ)
	}
}

func TestAND_IYd_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IY = 0x5000
	cpu.A = 0xFF
	cpu.WZ = 0xDEAD
	mem.Write(0x5003, 0xAA)
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0xA6) // AND (IY+d)
	mem.Write(0x8002, 0x03)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x5003 {
		t.Errorf("AND (IY+$03): WZ = $%04X, want $5003", cpu.WZ)
	}
}
