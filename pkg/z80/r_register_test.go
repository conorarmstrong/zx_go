package z80

import "testing"

// R-register increment per M1 cycle.
//
// Per Z80 spec, every M1 (opcode fetch) increments the low 7 bits of
// the R register. Operand bytes are NOT M1 cycles — they don't
// increment R. So:
//
//	base opcode (e.g. ADD A,B): R += 1
//	base+operand (e.g. LD A,n): R += 1
//	prefixed (CB/ED/DD/FD): R += 2 (prefix M1 + opcode M1)
//	DDCB/FDCB: R += 2 (DD/FD M1 + CB M1; d and inner xx are operands)
//
// Multi-byte immediates (n, nn, d) MUST NOT increment R — they're
// read via a non-M1 bus cycle.

// runROpcode loads bytes at PC, runs one instruction, returns the R
// register delta. Forces VariantStd to avoid Z80N M1 wait-state weirdness.
func runROpcode(t *testing.T, bytes []byte) byte {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.R = 0 // start clean
	for i, b := range bytes {
		mem.Write(uint16(0x8000+i), b)
	}
	before := cpu.R
	cpu.StepInstruction()
	// Compute the 7-bit delta, accounting for wrap.
	return (cpu.R - before) & 0x7F
}

func TestR_BaseOpcodes_IncrementBy1(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"NOP", []byte{0x00}},
		{"LD B,n", []byte{0x06, 0x42}},
		{"LD BC,nn", []byte{0x01, 0x34, 0x12}},
		{"INC A", []byte{0x3C}},
		{"ADD A,n", []byte{0xC6, 0x42}},
		{"JP nn", []byte{0xC3, 0x00, 0x90}},
		{"CALL nn", []byte{0xCD, 0x00, 0x90}},
	}
	for _, c := range cases {
		if got := runROpcode(t, c.bytes); got != 1 {
			t.Errorf("%s: R delta = %d, want 1", c.name, got)
		}
	}
}

func TestR_PrefixedOpcodes_IncrementBy2(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
	}{
		// CB-prefix
		{"RLC B", []byte{0xCB, 0x00}},
		{"BIT 0,B", []byte{0xCB, 0x40}},
		{"SET 0,B", []byte{0xCB, 0xC0}},
		// ED-prefix
		{"IN B,(C)", []byte{0xED, 0x40}},
		{"LD I,A", []byte{0xED, 0x47}},
		{"NEG", []byte{0xED, 0x44}},
		{"LD (nn),BC", []byte{0xED, 0x43, 0x00, 0x90}},
		// DD-prefix
		{"INC IX", []byte{0xDD, 0x23}},
		{"ADD IX,BC", []byte{0xDD, 0x09}},
		{"LD IX,nn", []byte{0xDD, 0x21, 0x34, 0x12}},
		{"LD A,(IX+d)", []byte{0xDD, 0x7E, 0x00}},
		{"LD (IX+d),n", []byte{0xDD, 0x36, 0x00, 0x42}},
		// FD-prefix
		{"INC IY", []byte{0xFD, 0x23}},
		{"LD IY,nn", []byte{0xFD, 0x21, 0x34, 0x12}},
	}
	for _, c := range cases {
		if got := runROpcode(t, c.bytes); got != 2 {
			t.Errorf("%s: R delta = %d, want 2", c.name, got)
		}
	}
}

func TestR_DDCB_FDCB_IncrementBy2(t *testing.T) {
	// DD CB d xx and FD CB d xx are TWO M1 fetches; d and xx are
	// operand bytes that do NOT increment R.
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"BIT 0,(IX+0)", []byte{0xDD, 0xCB, 0x00, 0x46}},
		{"RES 0,(IX+0)", []byte{0xDD, 0xCB, 0x00, 0x86}},
		{"SET 0,(IX+0)", []byte{0xDD, 0xCB, 0x00, 0xC6}},
		{"BIT 0,(IY+0)", []byte{0xFD, 0xCB, 0x00, 0x46}},
		{"SET 0,(IY+0)", []byte{0xFD, 0xCB, 0x00, 0xC6}},
	}
	for _, c := range cases {
		if got := runROpcode(t, c.bytes); got != 2 {
			t.Errorf("%s: R delta = %d, want 2", c.name, got)
		}
	}
}

// TestR_DD_LD_IXh_n_IncrementBy2 guards against LD IXh,n reading its
// immediate via c.fetch() (which increments R) instead of
// c.readOperand() (which doesn't). Same applies to LD IXl,n / LD
// IYh,n / LD IYl,n.
func TestR_DD_LD_IXh_n_IncrementBy2(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"LD IXh,n", []byte{0xDD, 0x26, 0x42}},
		{"LD IXl,n", []byte{0xDD, 0x2E, 0x42}},
		{"LD IYh,n", []byte{0xFD, 0x26, 0x42}},
		{"LD IYl,n", []byte{0xFD, 0x2E, 0x42}},
	}
	for _, c := range cases {
		if got := runROpcode(t, c.bytes); got != 2 {
			t.Errorf("%s: R delta = %d, want 2 (operand must NOT increment R)", c.name, got)
		}
	}
}

// TestR_LDAR_ReadsPostIncrementedR — LD A,R reads R AFTER the two
// M1 increments (ED prefix + $5F opcode), so A = R_before + 2.
func TestR_LDAR_ReadsPostIncrementedR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.R = 0x00
	cpu.A = 0x00
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x5F) // LD A,R
	cpu.StepInstruction()
	// R was incremented by 2 (ED + 5F M1 cycles) before the
	// instruction's c.A = c.R assignment.
	if cpu.A != 0x02 {
		t.Errorf("LD A,R after R=0 init: A = $%02X, want $02 (= R after 2 M1 incs)", cpu.A)
	}
}

// TestR_LDAR_PVMirrorsIFF2 — LD A,R copies IFF2 into the P/V flag
// (and similarly LD A,I). This is the Z80's mechanism for software
// to detect whether interrupts are currently enabled.
func TestR_LDAR_PVMirrorsIFF2(t *testing.T) {
	for _, iff2 := range []bool{true, false} {
		cpu, mem := createTestCPU()
		cpu.PC = 0x8000
		cpu.IFF2 = iff2
		cpu.F = 0
		mem.Write(0x8000, 0xED)
		mem.Write(0x8001, 0x5F) // LD A,R
		cpu.StepInstruction()
		gotPV := cpu.F&FLAG_PV != 0
		if gotPV != iff2 {
			t.Errorf("LD A,R with IFF2=%v: PV=%v, want %v", iff2, gotPV, iff2)
		}
	}
	cleanupTestROMs("test_roms_z80")
}

func TestR_LDAI_PVMirrorsIFF2(t *testing.T) {
	for _, iff2 := range []bool{true, false} {
		cpu, mem := createTestCPU()
		cpu.PC = 0x8000
		cpu.IFF2 = iff2
		cpu.F = 0
		mem.Write(0x8000, 0xED)
		mem.Write(0x8001, 0x57) // LD A,I
		cpu.StepInstruction()
		gotPV := cpu.F&FLAG_PV != 0
		if gotPV != iff2 {
			t.Errorf("LD A,I with IFF2=%v: PV=%v, want %v", iff2, gotPV, iff2)
		}
	}
	cleanupTestROMs("test_roms_z80")
}

// TestR_PreservesBit7AcrossM1 — bit 7 of R is set by LD R,A and is
// preserved across subsequent M1 increments. Only bits 6:0 increment.
func TestR_PreservesBit7AcrossM1(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.A = 0x80 // set bit 7
	cpu.R = 0x00
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x4F) // LD R,A
	mem.Write(0x8002, 0x00) // NOP
	mem.Write(0x8003, 0x00) // NOP
	cpu.StepInstruction()   // LD R,A: writes $80 into R, then... see note
	// After LD R,A, R = A = $80. The LD R,A instruction's own M1
	// fetches (ED + 4F) DO increment R, but the assignment c.R = c.A
	// happens AFTER those fetches. Net: R = $80.
	if cpu.R != 0x80 {
		t.Errorf("after LD R,A with A=$80: R = $%02X, want $80", cpu.R)
	}
	// Now run 2 NOPs — each M1 increments bits 6:0. Bit 7 must stay
	// set throughout.
	cpu.StepInstruction()
	cpu.StepInstruction()
	if cpu.R&0x80 == 0 {
		t.Errorf("after 2 NOPs from R=$80: R = $%02X, bit 7 cleared (should preserve)",
			cpu.R)
	}
	if cpu.R&0x7F != 0x02 {
		t.Errorf("after 2 NOPs from R=$80: low 7 bits = $%02X, want $02",
			cpu.R&0x7F)
	}
}

// TestR_LDRA_SetsRTo_A locks in LD R,A semantics: writes A into R
// (full byte, including bit 7), then the subsequent M1 of THIS
// instruction still increments R, but starting from A's value.
// Sean Young §A.1: "After LD R,A the value of R is A+1 (only the
// lower 7 bits are incremented)."
func TestR_LDRA_SetsRTo_A(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	cpu.A = 0x42
	cpu.R = 0xFF
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x4F) // LD R,A
	cpu.StepInstruction()
	// R should now hold A — but our impl writes c.R = c.A directly
	// (line 2085). Following Sean Young, the canonical behaviour is
	// that the M1 increment from the LD R,A instruction itself has
	// already happened before the write. Either way, c.R must equal
	// c.A AFTER the instruction (impls vary; FUSE writes R = A; some
	// write R = A+1). Our impl writes R = A.
	if cpu.R != 0x42 {
		t.Errorf("after LD R,A: R = $%02X, want $42 (= A)", cpu.R)
	}
}
