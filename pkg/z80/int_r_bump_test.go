package z80

import "testing"

// Interrupt acceptance R-register bump.
//
// Per Sean Young's "Undocumented Z80 Documented" §5, the interrupt
// acceptance sequence includes one M1 cycle (for INTA + reading the
// vector byte), which bumps R like every other M1 fetch — for both
// INT and NMI.

func TestINT_BumpsRRegister(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.PC = 0x4000
	cpu.SP = 0xFF00
	cpu.IM = 1
	cpu.IFF1 = true
	rBefore := cpu.R
	cpu.interrupt()
	cleanupTestROMs("test_roms_z80")
	wantR := (rBefore & 0x80) | ((rBefore + 1) & 0x7F)
	if cpu.R != wantR {
		t.Errorf("INT R bump: R = $%02X, want $%02X", cpu.R, wantR)
	}
}

func TestNMI_BumpsRRegister(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.PC = 0x4000
	cpu.SP = 0xFF00
	rBefore := cpu.R
	cpu.NMI()
	cleanupTestROMs("test_roms_z80")
	wantR := (rBefore & 0x80) | ((rBefore + 1) & 0x7F)
	if cpu.R != wantR {
		t.Errorf("NMI R bump: R = $%02X, want $%02X", cpu.R, wantR)
	}
}
