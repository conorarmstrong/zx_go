package z80

import "testing"

// WZ/MEMPTR on interrupt acceptance + NMI.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4, interrupt
// acceptance behaves like a CALL: MEMPTR ← new PC. Same for NMI
// where the target is fixed at $0066.

func TestInterrupt_IM1_SetsWZ(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.PC = 0x4000
	cpu.SP = 0xFF00
	cpu.IM = 1
	cpu.IFF1 = true
	cpu.WZ = 0xDEAD
	cpu.interrupt()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x0038 {
		t.Errorf("INT IM1: WZ = $%04X, want $0038", cpu.WZ)
	}
}

func TestInterrupt_IM2_SetsWZ(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x4000
	cpu.SP = 0xFF00
	cpu.IM = 2
	cpu.I = 0x80
	cpu.IM2Vector = 0xFF
	cpu.IFF1 = true
	cpu.WZ = 0xDEAD
	// Vector table at $80FF, $8100 → $5678.
	mem.Write(0x80FF, 0x78)
	mem.Write(0x8100, 0x56)
	cpu.interrupt()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x5678 {
		t.Errorf("INT IM2: WZ = $%04X, want $5678", cpu.WZ)
	}
}

func TestNMI_SetsWZ(t *testing.T) {
	cpu, _ := createTestCPU()
	cpu.PC = 0x4000
	cpu.SP = 0xFF00
	cpu.WZ = 0xDEAD
	cpu.NMI()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x0066 {
		t.Errorf("NMI: WZ = $%04X, want $0066", cpu.WZ)
	}
}
