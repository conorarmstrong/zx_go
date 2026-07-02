package z80

import "testing"

// DD / FD "wasted prefix" behavior.
//
// Per Sean Young §5.7: a DD or FD prefix followed by an opcode
// that doesn't use the indexed register is a NONI ("no-operation,
// no-interrupts"): the prefix consumes 4 T-states + one M1 cycle
// (R bump) and the underlying base opcode executes normally.
//
// Same applies to chained DD-DD / DD-FD / FD-DD / FD-FD: every
// prefix in the chain costs 4 t + 1 M1 cycle; only the FINAL
// prefix actually modifies the next opcode.

func setupNONI(t *testing.T, bytes ...byte) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.tstates = 0
	for i, b := range bytes {
		mem.Write(uint16(0x8000+i), b)
	}
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu
}

func TestDD_FollowedByNOP_Costs8t(t *testing.T) {
	// DD 00 — DD prefix is wasted, then NOP. Real Z80: 4+4 = 8 t.
	cpu := setupNONI(t, 0xDD, 0x00)
	if cpu.tstates != 8 {
		t.Errorf("DD NOP: tstates = %d, want 8", cpu.tstates)
	}
}

func TestFD_FollowedByNOP_Costs8t(t *testing.T) {
	cpu := setupNONI(t, 0xFD, 0x00)
	if cpu.tstates != 8 {
		t.Errorf("FD NOP: tstates = %d, want 8", cpu.tstates)
	}
}
