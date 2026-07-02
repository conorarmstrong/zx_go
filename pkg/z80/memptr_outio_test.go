package z80

import "testing"

// WZ/MEMPTR updates for block output OUTI / OUTD.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4 / §4.2:
//   OUTI: WZ = (B-1, C) + 1 — BC at IO time uses post-decrement B
//   OUTD: WZ = (B-1, C) - 1
//
// On real Z80, B is decremented BEFORE the IO write happens; the
// port-address bus carries the post-decrement value. The MEMPTR
// update uses that post-decrement BC.

func runBlockOut(t *testing.T, op byte, bc uint16, wzBefore uint16) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.setBC(bc)
	cpu.setHL(0xC000)
	cpu.WZ = wzBefore
	mem.Write(0xC000, 0x55)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestOUTI_WZIsPostBcPlus1(t *testing.T) {
	// B=$12, C=$FE. After B--: B=$11. New BC = $11FE. WZ = $11FF.
	cpu := runBlockOut(t, 0xA3, 0x12FE, 0xDEAD)
	if cpu.WZ != 0x11FF {
		t.Errorf("OUTI: WZ = $%04X, want $11FF", cpu.WZ)
	}
}

func TestOUTD_WZIsPostBcMinus1(t *testing.T) {
	// B=$12, C=$00. After B--: B=$11. New BC = $1100. WZ = $10FF.
	cpu := runBlockOut(t, 0xAB, 0x1200, 0xDEAD)
	if cpu.WZ != 0x10FF {
		t.Errorf("OUTD: WZ = $%04X, want $10FF", cpu.WZ)
	}
}
