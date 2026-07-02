package z80

import "testing"

// ED-prefix NEG mirror encodings.
//
// Per Sean Young's "Undocumented Z80 Documented" §A.1, ED $44, $4C,
// $54, $5C, $64, $6C, $74, $7C are all NEG mirror encodings — they
// all negate the accumulator with identical flag semantics and the
// same 8 T-state cost.
//
// Our prior impl had only $44 as NEG; the 7 mirrors were stubbed as
// NOPs. Software using one of the mirror encodings (e.g. via
// hand-rolled assembly or buggy assemblers) would have seen
// "no-op" instead of negation — a latent functional divergence
// that this iter pins+fixes.

func runNegMirror(t *testing.T, op byte, initA byte) (byte, byte) {
	t.Helper()
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.A = initA
	cpu.F = 0
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu.A, cpu.F
}

// TestNEG_AllEightMirrorsNegateA — every encoding produces the same
// negation result.
func TestNEG_AllEightMirrorsNegateA(t *testing.T) {
	mirrors := []byte{0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C}
	for _, op := range mirrors {
		got, _ := runNegMirror(t, op, 0x42)
		// -0x42 in two's complement = 0xBE
		if got != 0xBE {
			t.Errorf("NEG mirror ED $%02X with A=$42: A = $%02X, want $BE", op, got)
		}
	}
}

// TestNEG_ZeroBoundary — NEG on A=0 yields 0 with C=0, Z=1.
func TestNEG_ZeroBoundary(t *testing.T) {
	mirrors := []byte{0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C}
	for _, op := range mirrors {
		got, flags := runNegMirror(t, op, 0x00)
		if got != 0x00 {
			t.Errorf("NEG mirror ED $%02X with A=0: A = $%02X, want 0", op, got)
		}
		if flags&FLAG_C != 0 {
			t.Errorf("NEG mirror ED $%02X with A=0: C set, want clear", op)
		}
		if flags&FLAG_Z == 0 {
			t.Errorf("NEG mirror ED $%02X with A=0: Z clear, want set", op)
		}
		if flags&FLAG_N == 0 {
			t.Errorf("NEG mirror ED $%02X with A=0: N clear, want set (always)", op)
		}
	}
}

// TestNEG_OverflowAt80 — A=$80 stays $80 but sets PV (overflow).
func TestNEG_OverflowAt80(t *testing.T) {
	mirrors := []byte{0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C}
	for _, op := range mirrors {
		got, flags := runNegMirror(t, op, 0x80)
		if got != 0x80 {
			t.Errorf("NEG mirror ED $%02X with A=$80: A = $%02X, want $80", op, got)
		}
		if flags&FLAG_PV == 0 {
			t.Errorf("NEG mirror ED $%02X with A=$80: PV clear, want set (overflow)", op)
		}
		if flags&FLAG_C == 0 {
			t.Errorf("NEG mirror ED $%02X with A=$80: C clear, want set", op)
		}
	}
}
