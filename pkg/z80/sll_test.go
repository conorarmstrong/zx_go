package z80

import (
	"testing"
)

// SLL / SLI undocumented opcode tests.
//
// SLL ("Shift Left Logical" — sometimes called SLI for "Shift Left
// and Insert") is the CB-prefix opcode set $30-$37 (one per register
// + (HL)). Not in the official Zilog manual but documented in
// Sean Young's "Undocumented Z80 Documented" §3.3 and FUSE z80ops.c.
//
// Semantics:
//   result = (val << 1) | 1   ; bit 0 is forced to 1
//   C = bit 7 of original val (shifted out)
//   N = 0
//   PV = parity of result
//   H = 0
//   Z = 1 iff result == 0  (impossible since bit 0 = 1, so always 0)
//   S = bit 7 of result
//   F3 = bit 3 of result, F5 = bit 5 of result
//
// Per-register opcodes: $CB30 SLL B, $CB31 SLL C, $CB32 SLL D,
// $CB33 SLL E, $CB34 SLL H, $CB35 SLL L, $CB36 SLL (HL), $CB37 SLL A.

// testSLLViaCBOpcode runs the instruction pair CB,30+r at PC and
// returns post-execution CPU.
func runSLL(t *testing.T, regSelector byte, initialVal byte) *CPU {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Plant CB-prefix SLL opcode at $8000.
	mem.Write(0x8000, 0xCB)
	mem.Write(0x8001, 0x30|regSelector)
	cpu.PC = 0x8000

	// Set the target register according to the encoding selector.
	switch regSelector {
	case 0:
		cpu.B = initialVal
	case 1:
		cpu.C = initialVal
	case 2:
		cpu.D = initialVal
	case 3:
		cpu.E = initialVal
	case 4:
		cpu.H = initialVal
	case 5:
		cpu.L = initialVal
	case 6:
		// (HL) variant: write the byte at HL = $9000.
		cpu.H = 0x90
		cpu.L = 0x00
		mem.Write(0x9000, initialVal)
	case 7:
		cpu.A = initialVal
	}

	cpu.StepInstruction()
	return cpu
}

// TestSLL_BitInsertedAtPos0 — bit 0 of result MUST be 1 regardless
// of input.
func TestSLL_BitInsertedAtPos0(t *testing.T) {
	for _, v := range []byte{0x00, 0xFF, 0x55, 0xAA, 0x01, 0x80} {
		cpu := runSLL(t, 7, v) // SLL A
		if cpu.A&0x01 == 0 {
			t.Errorf("SLL A input $%02X: result $%02X has bit 0 = 0; should be forced 1",
				v, cpu.A)
		}
	}
}

// TestSLL_CarryFromBit7 — C flag = bit 7 of original.
func TestSLL_CarryFromBit7(t *testing.T) {
	for _, tc := range []struct {
		in   byte
		want bool
	}{
		{0x00, false},
		{0x7F, false},
		{0x80, true},
		{0xFF, true},
	} {
		cpu := runSLL(t, 7, tc.in)
		gotC := cpu.F&FLAG_C != 0
		if gotC != tc.want {
			t.Errorf("SLL A $%02X: C=%v, want %v", tc.in, gotC, tc.want)
		}
	}
}

// TestSLL_NeverZero — because bit 0 is forced to 1, the result can
// never be zero, so Z is always cleared. Verify.
func TestSLL_NeverZero(t *testing.T) {
	for _, v := range []byte{0x00, 0xFF, 0x80, 0x01, 0x55, 0xAA} {
		cpu := runSLL(t, 7, v)
		if cpu.F&FLAG_Z != 0 {
			t.Errorf("SLL A $%02X: Z set unexpectedly (result was $%02X)",
				v, cpu.A)
		}
	}
}

// TestSLL_ClearsNandH — N and H flags must be cleared per spec.
func TestSLL_ClearsNandH(t *testing.T) {
	cpu := runSLL(t, 7, 0x55)
	if cpu.F&FLAG_N != 0 {
		t.Errorf("SLL A: N should be 0, got F=$%02X", cpu.F)
	}
	if cpu.F&FLAG_H != 0 {
		t.Errorf("SLL A: H should be 0, got F=$%02X", cpu.F)
	}
}

// TestSLL_AllRegisters_BNN — SLL B/C/D/E/H/L/A each write back to
// their own register, leaving others untouched.
func TestSLL_AllRegisters_WriteBack(t *testing.T) {
	for sel := byte(0); sel <= 7; sel++ {
		if sel == 6 {
			continue // (HL) handled separately
		}
		cpu := runSLL(t, sel, 0x40)
		want := byte((0x40 << 1) | 1)
		var got byte
		var name string
		switch sel {
		case 0:
			got = cpu.B
			name = "B"
		case 1:
			got = cpu.C
			name = "C"
		case 2:
			got = cpu.D
			name = "D"
		case 3:
			got = cpu.E
			name = "E"
		case 4:
			got = cpu.H
			name = "H"
		case 5:
			got = cpu.L
			name = "L"
		case 7:
			got = cpu.A
			name = "A"
		}
		if got != want {
			t.Errorf("SLL %s of $40: result = $%02X, want $%02X", name, got, want)
		}
	}
}

// TestSLL_MemoryVariant — SLL (HL) reads the byte at HL, computes,
// writes back, and updates flags.
func TestSLL_MemoryVariant_HL(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x8000, 0xCB)
	mem.Write(0x8001, 0x36) // SLL (HL)
	cpu.PC = 0x8000
	cpu.H = 0x90
	cpu.L = 0x10
	mem.Write(0x9010, 0x12)

	cpu.StepInstruction()

	want := byte((0x12 << 1) | 1) // = $25
	if got := mem.Read(0x9010); got != want {
		t.Errorf("SLL (HL): mem[$9010] = $%02X, want $%02X", got, want)
	}
	if cpu.F&FLAG_C != 0 {
		t.Errorf("SLL (HL) $12: C should be 0, got F=$%02X", cpu.F)
	}
}

// TestSLL_TStateCost — base CB-prefix opcode is 8 t-states for
// register variants, 15 for (HL).
func TestSLL_TStateCost(t *testing.T) {
	for _, tc := range []struct {
		sel  byte
		want uint64
		name string
	}{
		{0, 8, "SLL B"},
		{7, 8, "SLL A"},
		{6, 15, "SLL (HL)"},
	} {
		cpu, mem := createTestCPU()
		defer cleanupTestROMs("test_roms_z80")
		mem.Write(0x8000, 0xCB)
		mem.Write(0x8001, 0x30|tc.sel)
		cpu.PC = 0x8000
		if tc.sel == 6 {
			cpu.H = 0x90
			cpu.L = 0x00
			mem.Write(0x9000, 0x55)
		}
		before := cpu.tstates
		cpu.StepInstruction()
		got := cpu.tstates - before
		if got != tc.want {
			t.Errorf("%s: t-states = %d, want %d", tc.name, got, tc.want)
		}
	}
}
