package z80

import "testing"

// WZ/MEMPTR for DDCB / FDCB indexed instructions.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4, every DDCB /
// FDCB instruction sets MEMPTR = IX+d (or IY+d) as part of the
// effective-address calculation. Without this update, a subsequent
// BIT n,(HL) reads stale F3/F5 from WZ_high.
//
// DDCB layout: DD CB <d> <op>
// FDCB layout: FD CB <d> <op>

func runDDCB(t *testing.T, ix uint16, d byte, op byte, initWZ uint16) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IX = ix
	cpu.WZ = initWZ
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0xCB)
	mem.Write(0x8002, d)
	mem.Write(0x8003, op)
	// Plant a value at the effective address so the op has something
	// to read.
	mem.Write(ix+uint16(int8(d)), 0x55)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func runFDCB(t *testing.T, iy uint16, d byte, op byte, initWZ uint16) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IY = iy
	cpu.WZ = initWZ
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0xCB)
	mem.Write(0x8002, d)
	mem.Write(0x8003, op)
	mem.Write(iy+uint16(int8(d)), 0x55)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestDDCB_WZIsIXPlusD_BIT(t *testing.T) {
	cpu := runDDCB(t, 0x4000, 0x10, 0x46, 0xDEAD) // BIT 0,(IX+$10)
	if cpu.WZ != 0x4010 {
		t.Errorf("DDCB BIT: WZ = $%04X, want $4010", cpu.WZ)
	}
}

func TestDDCB_WZIsIXPlusD_SET(t *testing.T) {
	cpu := runDDCB(t, 0x4000, 0x20, 0xC6, 0xDEAD) // SET 0,(IX+$20)
	if cpu.WZ != 0x4020 {
		t.Errorf("DDCB SET: WZ = $%04X, want $4020", cpu.WZ)
	}
}

func TestDDCB_WZIsIXPlusD_RES(t *testing.T) {
	cpu := runDDCB(t, 0x4000, 0x30, 0x86, 0xDEAD) // RES 0,(IX+$30)
	if cpu.WZ != 0x4030 {
		t.Errorf("DDCB RES: WZ = $%04X, want $4030", cpu.WZ)
	}
}

func TestDDCB_WZIsIXPlusD_RLC(t *testing.T) {
	cpu := runDDCB(t, 0x4000, 0x05, 0x06, 0xDEAD) // RLC (IX+$05)
	if cpu.WZ != 0x4005 {
		t.Errorf("DDCB RLC: WZ = $%04X, want $4005", cpu.WZ)
	}
}

func TestDDCB_WZNegativeDisplacement(t *testing.T) {
	// d = $FF (= -1 as signed). IX+d = $4000 + (-1) = $3FFF.
	cpu := runDDCB(t, 0x4000, 0xFF, 0x46, 0xDEAD)
	if cpu.WZ != 0x3FFF {
		t.Errorf("DDCB negative-d: WZ = $%04X, want $3FFF", cpu.WZ)
	}
}

func TestFDCB_WZIsIYPlusD_BIT(t *testing.T) {
	cpu := runFDCB(t, 0x5000, 0x08, 0x46, 0xDEAD)
	if cpu.WZ != 0x5008 {
		t.Errorf("FDCB BIT: WZ = $%04X, want $5008", cpu.WZ)
	}
}

func TestFDCB_WZIsIYPlusD_SET(t *testing.T) {
	cpu := runFDCB(t, 0x5000, 0x10, 0xC6, 0xDEAD)
	if cpu.WZ != 0x5010 {
		t.Errorf("FDCB SET: WZ = $%04X, want $5010", cpu.WZ)
	}
}
