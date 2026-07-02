package z80

import "testing"

// WZ/MEMPTR on repeating block instructions.
//
// Per Sean Young's "Undocumented Z80 Documented" §4.2: every
// repeating block instruction (LDIR / LDDR / CPIR / CPDR /
// INIR / INDR / OTIR / OTDR) sets MEMPTR = PC + 1 on each
// non-final iteration. PC at that point has been adjusted by
// -2 to point back at the ED prefix, so MEMPTR = address of the
// LDIR/etc opcode byte.
//
// On the FINAL iteration (BC == 0 for LDIR/LDDR/INIR/INDR/OTIR/
// OTDR, BC == 0 || Z for CPIR/CPDR), MEMPTR is left at whatever
// the underlying LDI/CPI/INI/OUTI set (or unchanged for LDI/LDD).

func setupRepeat(t *testing.T, op byte, bc uint16, initWZ uint16) (*CPU, *uint16) {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.setBC(bc)
	cpu.setHL(0x4000)
	cpu.setDE(0x5000)
	cpu.A = 0x42
	cpu.WZ = initWZ
	mem.Write(0x4000, 0x55)
	mem.Write(0x4001, 0x66)
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	pcOfOpcode := uint16(0x8000)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu, &pcOfOpcode
}

func TestLDIR_RepeatWZIsOpcodePlus1(t *testing.T) {
	// BC=2 → first iteration: BC becomes 1, instruction repeats,
	// so PC -= 2 (back to $8000) and WZ should be $8001.
	cpu, _ := setupRepeat(t, 0xB0, 2, 0xDEAD)
	if cpu.WZ != 0x8001 {
		t.Errorf("LDIR repeat: WZ = $%04X, want $8001", cpu.WZ)
	}
}

func TestLDDR_RepeatWZIsOpcodePlus1(t *testing.T) {
	cpu, _ := setupRepeat(t, 0xB8, 2, 0xDEAD)
	if cpu.WZ != 0x8001 {
		t.Errorf("LDDR repeat: WZ = $%04X, want $8001", cpu.WZ)
	}
}

func TestLDIR_FinalIterLeavesWZ(t *testing.T) {
	// BC=1 → only one iteration, BC becomes 0, no repeat. WZ
	// is left unchanged from LDI (which does not update WZ).
	cpu, _ := setupRepeat(t, 0xB0, 1, 0xDEAD)
	if cpu.WZ != 0xDEAD {
		t.Errorf("LDIR final iter: WZ = $%04X, want $DEAD (unchanged)", cpu.WZ)
	}
}

func TestCPIR_RepeatWZ(t *testing.T) {
	// A=$42, (HL)=$55 → no match, BC=2 → repeats. CPI sets
	// WZ += 1 (so WZ becomes initWZ+1), then repeat fires and
	// overwrites with PC+1 = $8001.
	cpu, _ := setupRepeat(t, 0xB1, 2, 0xDEAD)
	if cpu.WZ != 0x8001 {
		t.Errorf("CPIR repeat: WZ = $%04X, want $8001", cpu.WZ)
	}
}

func TestCPDR_RepeatWZ(t *testing.T) {
	cpu, _ := setupRepeat(t, 0xB9, 2, 0xDEAD)
	if cpu.WZ != 0x8001 {
		t.Errorf("CPDR repeat: WZ = $%04X, want $8001", cpu.WZ)
	}
}
