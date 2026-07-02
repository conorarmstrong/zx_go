package z80

import "testing"

// Miscellaneous flag-edge tests for SCF / CCF / CPL.
// Sean Young's "Undocumented Z80 Documented" pins F5/F3 behaviour
// on these instructions; getting them wrong is invisible to most
// software but breaks the standard z80flags test suite.

// scfRun runs SCF starting from given A and F.
func scfRun(a, f byte) (byte, byte) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.A = a
	cpu.F = f
	mem.Write(0x8000, 0x37) // SCF
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu.A, cpu.F
}

// ccfRun runs CCF starting from given A and F.
func ccfRun(a, f byte) (byte, byte) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.A = a
	cpu.F = f
	mem.Write(0x8000, 0x3F) // CCF
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu.A, cpu.F
}

// cplRun runs CPL starting from given A and F.
func cplRun(a, f byte) (byte, byte) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	cpu.A = a
	cpu.F = f
	mem.Write(0x8000, 0x2F) // CPL
	cpu.StepInstruction()
	cleanupTestROMs("test_roms_z80")
	return cpu.A, cpu.F
}

// TestSCF_FlagShape — SCF sets C=1, clears H+N, preserves S/Z/PV,
// F5/F3 come from A. Most basic post-SCF state, no prior-instruction
// quirks.
func TestSCF_FlagShape(t *testing.T) {
	// A = $A0 (bit 5 set, bit 3 clear), F = SZPV all set.
	preF := byte(FLAG_S | FLAG_Z | FLAG_PV | FLAG_H | FLAG_N)
	_, postF := scfRun(0xA0, preF)

	if postF&FLAG_C == 0 {
		t.Errorf("SCF: C cleared, want set")
	}
	if postF&FLAG_H != 0 {
		t.Errorf("SCF: H set, want clear")
	}
	if postF&FLAG_N != 0 {
		t.Errorf("SCF: N set, want clear")
	}
	if postF&FLAG_S == 0 {
		t.Errorf("SCF: S clear, want preserved (= set)")
	}
	if postF&FLAG_Z == 0 {
		t.Errorf("SCF: Z clear, want preserved (= set)")
	}
	if postF&FLAG_PV == 0 {
		t.Errorf("SCF: PV clear, want preserved (= set)")
	}
	// F5 from A bit 5 = $A0 → 1
	if postF&FLAG_F5 == 0 {
		t.Errorf("SCF F5: clear, want set (A bit 5 = 1)")
	}
	// F3 from A bit 3 = $A0 → 0
	if postF&FLAG_F3 != 0 {
		t.Errorf("SCF F3: set, want clear (A bit 3 = 0)")
	}
}

// TestCCF_ToggleC — CCF flips C, and the previous C ends up in H.
func TestCCF_ToggleC(t *testing.T) {
	// Start C=1.
	_, postF := ccfRun(0x00, FLAG_C)
	if postF&FLAG_C != 0 {
		t.Errorf("CCF with C=1 in: C still set, want clear")
	}
	if postF&FLAG_H == 0 {
		t.Errorf("CCF with C=1 in: H clear, want set (= old C)")
	}
	if postF&FLAG_N != 0 {
		t.Errorf("CCF: N set, want clear")
	}
	// Start C=0.
	_, postF = ccfRun(0x00, 0)
	if postF&FLAG_C == 0 {
		t.Errorf("CCF with C=0 in: C clear, want set")
	}
	if postF&FLAG_H != 0 {
		t.Errorf("CCF with C=0 in: H set, want clear (= old C)")
	}
}

func TestCCF_PreservesSZPV(t *testing.T) {
	preF := byte(FLAG_S | FLAG_Z | FLAG_PV)
	_, postF := ccfRun(0x00, preF)
	if postF&FLAG_S == 0 || postF&FLAG_Z == 0 || postF&FLAG_PV == 0 {
		t.Errorf("CCF should preserve S/Z/PV; postF=$%02X", postF)
	}
}

func TestCCF_F5F3FromA(t *testing.T) {
	// A = $08 (bit 3 set, bit 5 clear).
	_, postF := ccfRun(0x08, 0)
	if postF&FLAG_F3 == 0 {
		t.Errorf("CCF F3: clear, want set (A bit 3 = 1)")
	}
	if postF&FLAG_F5 != 0 {
		t.Errorf("CCF F5: set, want clear (A bit 5 = 0)")
	}
}

// TestCPL_FlagShape — CPL complements A, sets H + N, preserves
// S/Z/PV/C, F5/F3 come from the NEW A (post-complement).
func TestCPL_FlagShape(t *testing.T) {
	// A = $55 → CPL → A = $AA.
	postA, postF := cplRun(0x55, FLAG_S|FLAG_Z|FLAG_PV|FLAG_C)
	if postA != 0xAA {
		t.Errorf("CPL: A = $%02X, want $AA", postA)
	}
	// S/Z/PV/C preserved.
	if postF&FLAG_S == 0 || postF&FLAG_Z == 0 || postF&FLAG_PV == 0 || postF&FLAG_C == 0 {
		t.Errorf("CPL should preserve S/Z/PV/C; postF=$%02X", postF)
	}
	// H + N set.
	if postF&FLAG_H == 0 {
		t.Errorf("CPL: H clear, want set")
	}
	if postF&FLAG_N == 0 {
		t.Errorf("CPL: N clear, want set")
	}
	// F5/F3 from new A = $AA → bit 5 = 1, bit 3 = 1.
	if postF&FLAG_F5 == 0 {
		t.Errorf("CPL F5: clear, want set (new A=$AA bit 5)")
	}
	if postF&FLAG_F3 == 0 {
		t.Errorf("CPL F3: clear, want set (new A=$AA bit 3)")
	}
}

// TestSCF_CCF_F5F3VariousA — sweep A through 4 values that exercise
// every (F5, F3) combination.
func TestSCF_CCF_F5F3VariousA(t *testing.T) {
	cases := []struct {
		a              byte
		wantF5, wantF3 bool
	}{
		{0x00, false, false},
		{0x08, false, true},
		{0x20, true, false},
		{0x28, true, true},
		{0xFF, true, true},
	}
	for _, c := range cases {
		_, postF := scfRun(c.a, 0)
		gotF5 := postF&FLAG_F5 != 0
		gotF3 := postF&FLAG_F3 != 0
		if gotF5 != c.wantF5 || gotF3 != c.wantF3 {
			t.Errorf("SCF A=$%02X: F5=%v F3=%v, want F5=%v F3=%v",
				c.a, gotF5, gotF3, c.wantF5, c.wantF3)
		}
		_, postF = ccfRun(c.a, 0)
		gotF5 = postF&FLAG_F5 != 0
		gotF3 = postF&FLAG_F3 != 0
		if gotF5 != c.wantF5 || gotF3 != c.wantF3 {
			t.Errorf("CCF A=$%02X: F5=%v F3=%v, want F5=%v F3=%v",
				c.a, gotF5, gotF3, c.wantF5, c.wantF3)
		}
	}
}
