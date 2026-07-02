package z80

import "testing"

// WZ/MEMPTR updates for RLD / RRD.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4, both
// instructions set MEMPTR = HL + 1 as part of their indirect memory
// access. Without this update, a following BIT n,(HL) reads stale
// F3/F5 from WZ_high.

func runRxD(t *testing.T, op byte, initA byte, hl uint16, memVal byte, wzBefore uint16) (cpu *CPU) {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.A = initA
	cpu.setHL(hl)
	cpu.WZ = wzBefore
	mem.Write(hl, memVal)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestRRD_WZIsHLPlus1(t *testing.T) {
	cpu := runRxD(t, 0x67, 0x84, 0x4000, 0x20, 0xDEAD)
	if cpu.WZ != 0x4001 {
		t.Errorf("RRD: WZ = $%04X, want $4001", cpu.WZ)
	}
}

func TestRLD_WZIsHLPlus1(t *testing.T) {
	cpu := runRxD(t, 0x6F, 0x84, 0x4000, 0x20, 0xDEAD)
	if cpu.WZ != 0x4001 {
		t.Errorf("RLD: WZ = $%04X, want $4001", cpu.WZ)
	}
}
