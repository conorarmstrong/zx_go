package z80

import "testing"

// ED-prefix IM mirror encodings.
//
// Per Sean Young's "Undocumented Z80 Documented" §A.1:
//   IM 0: ED $46, $4E, $66, $6E
//   IM 1: ED $56, $76
//   IM 2: ED $5E, $7E
//
// Our prior impl handled only the canonical $46/$56/$5E; the five
// mirrors fell through to the default NONI-NOP. Buggy software (or
// hand-rolled assemblers) using a mirror encoding silently did
// nothing instead of changing interrupt mode.

func runIMMirror(t *testing.T, op byte, initIM byte) byte {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.IM = initIM
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu.IM
}

func TestIM0_AllMirrorsSetMode(t *testing.T) {
	mirrors := []byte{0x46, 0x4E, 0x66, 0x6E}
	for _, op := range mirrors {
		got := runIMMirror(t, op, 2)
		if got != 0 {
			t.Errorf("IM 0 mirror ED $%02X: IM = %d, want 0", op, got)
		}
	}
}

func TestIM1_AllMirrorsSetMode(t *testing.T) {
	mirrors := []byte{0x56, 0x76}
	for _, op := range mirrors {
		got := runIMMirror(t, op, 0)
		if got != 1 {
			t.Errorf("IM 1 mirror ED $%02X: IM = %d, want 1", op, got)
		}
	}
}

func TestIM2_AllMirrorsSetMode(t *testing.T) {
	mirrors := []byte{0x5E, 0x7E}
	for _, op := range mirrors {
		got := runIMMirror(t, op, 0)
		if got != 2 {
			t.Errorf("IM 2 mirror ED $%02X: IM = %d, want 2", op, got)
		}
	}
}
