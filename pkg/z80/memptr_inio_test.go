package z80

import "testing"

// WZ/MEMPTR updates for block input INI / IND.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4 / §4.2:
//   INI: WZ = BC + 1 (BC read at port-access time, pre-decrement of B)
//   IND: WZ = BC - 1
//
// On real Z80, the IO read at port BC happens BEFORE B is
// decremented, so BC at that instant is the original (pre-instruction)
// BC. The MEMPTR update uses that BC.

func runBlockIn(t *testing.T, op byte, bc uint16, wzBefore uint16) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.setBC(bc)
	cpu.setHL(0xC000)
	cpu.WZ = wzBefore
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestINI_WZIsBCPlus1(t *testing.T) {
	cpu := runBlockIn(t, 0xA2, 0x12FE, 0xDEAD) // INI
	if cpu.WZ != 0x12FF {
		t.Errorf("INI: WZ = $%04X, want $12FF", cpu.WZ)
	}
}

func TestIND_WZIsBCMinus1(t *testing.T) {
	cpu := runBlockIn(t, 0xAA, 0x1200, 0xDEAD) // IND
	if cpu.WZ != 0x11FF {
		t.Errorf("IND: WZ = $%04X, want $11FF", cpu.WZ)
	}
}
