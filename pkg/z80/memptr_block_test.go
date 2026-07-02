package z80

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// WZ/MEMPTR updates for block compare instructions.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.4:
//   CPI: WZ += 1
//   CPD: WZ -= 1
//
// These are spec-mandated MEMPTR updates that affect F3/F5 of any
// subsequent BIT n,(HL). Without them, software that compares
// fingerprints with a CPI/CPD then a BIT n,(HL) (a common idiom)
// will see the wrong undoc-flag pattern.

func setupCPxTest(t *testing.T, op byte, wzBefore uint16) (cpu *CPU, mem *memory.Memory) {
	t.Helper()
	cpu, mem = createTestCPU()
	cpu.PC = 0x8000
	cpu.A = 0x42
	cpu.setHL(0x4000)
	cpu.setBC(0x0002)
	cpu.WZ = wzBefore
	mem.Write(0x4000, 0x42)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	return
}

func TestCPI_WZIncrement(t *testing.T) {
	cpu, _ := setupCPxTest(t, 0xA1, 0x1234)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x1235 {
		t.Errorf("CPI: WZ = $%04X, want $1235", cpu.WZ)
	}
}

func TestCPD_WZDecrement(t *testing.T) {
	cpu, _ := setupCPxTest(t, 0xA9, 0x1234)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	if cpu.WZ != 0x1233 {
		t.Errorf("CPD: WZ = $%04X, want $1233", cpu.WZ)
	}
}
