package z80

import "testing"

// classifyBranch test matrix.
//
// classifyBranch attributes each PC transition to a BranchSource
// category for the debugger's history view. Sources:
//   1 = JP / JP (HL) / JP (IX) / JP (IY) / JP cc taken
//   2 = JR / DJNZ taken
//   3 = CALL / CALL cc taken
//   4 = RET / RET cc taken / RETI / RETN
//   5 = RST n
//   6/7/8 = reserved for NMI / interrupt / reset (skipped here —
//           classifyBranch returns early if BranchSource >= 6)
//
// The function is heuristic: it infers branch-vs-fallthrough from
// PC delta because the actual instruction was already decoded.
// Tests pin every encoding family so a regression in PC accounting
// inside the executor shows up as a classification miss.

// classifyCase runs classifyBranch with a freshly-zeroed source.
// Returns the source it picked.
func classifyCase(cpu *CPU, prevPC, newPC uint16, opcode byte) byte {
	cpu.BranchSource = 0
	cpu.PC = newPC
	cpu.classifyBranch(prevPC, opcode)
	return cpu.BranchSource
}

func TestClassifyBranch_UnconditionalJP(t *testing.T) {
	cpu, _ := createTestCPU()
	if got := classifyCase(cpu, 0x1000, 0x8000, 0xC3); got != 1 {
		t.Errorf("JP nn classified as %d, want 1", got)
	}
	if got := classifyCase(cpu, 0x1000, 0x8000, 0xE9); got != 1 {
		t.Errorf("JP (HL) classified as %d, want 1", got)
	}
}

func TestClassifyBranch_ConditionalJPTaken(t *testing.T) {
	cpu, _ := createTestCPU()
	// All 8 cc JP opcodes. PC moved (not prevPC+3) → taken.
	for _, op := range []byte{0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA} {
		if got := classifyCase(cpu, 0x1000, 0x9000, op); got != 1 {
			t.Errorf("JP cc op=$%02X taken: classified %d, want 1", op, got)
		}
		// Not taken: PC = prevPC + 3 → source untouched (0).
		if got := classifyCase(cpu, 0x1000, 0x1003, op); got != 0 {
			t.Errorf("JP cc op=$%02X not-taken: classified %d, want 0", op, got)
		}
	}
}

func TestClassifyBranch_JR(t *testing.T) {
	cpu, _ := createTestCPU()
	if got := classifyCase(cpu, 0x1000, 0x1020, 0x18); got != 2 {
		t.Errorf("JR classified as %d, want 2", got)
	}
	// JR cc + DJNZ ($10).
	for _, op := range []byte{0x20, 0x28, 0x30, 0x38, 0x10} {
		if got := classifyCase(cpu, 0x1000, 0x1020, op); got != 2 {
			t.Errorf("op=$%02X taken: classified %d, want 2", op, got)
		}
		// Not taken: PC = prevPC + 2.
		if got := classifyCase(cpu, 0x1000, 0x1002, op); got != 0 {
			t.Errorf("op=$%02X not-taken: classified %d, want 0", op, got)
		}
	}
}

func TestClassifyBranch_Call(t *testing.T) {
	cpu, _ := createTestCPU()
	if got := classifyCase(cpu, 0x1000, 0x8000, 0xCD); got != 3 {
		t.Errorf("CALL nn classified as %d, want 3", got)
	}
	for _, op := range []byte{0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC} {
		if got := classifyCase(cpu, 0x1000, 0x8000, op); got != 3 {
			t.Errorf("CALL cc op=$%02X taken: classified %d, want 3", op, got)
		}
		if got := classifyCase(cpu, 0x1000, 0x1003, op); got != 0 {
			t.Errorf("CALL cc op=$%02X not-taken: classified %d, want 0", op, got)
		}
	}
}

func TestClassifyBranch_Ret(t *testing.T) {
	cpu, _ := createTestCPU()
	if got := classifyCase(cpu, 0x1000, 0x8000, 0xC9); got != 4 {
		t.Errorf("RET classified as %d, want 4", got)
	}
	for _, op := range []byte{0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0, 0xF8} {
		if got := classifyCase(cpu, 0x1000, 0x8000, op); got != 4 {
			t.Errorf("RET cc op=$%02X taken: classified %d, want 4", op, got)
		}
		if got := classifyCase(cpu, 0x1000, 0x1001, op); got != 0 {
			t.Errorf("RET cc op=$%02X not-taken: classified %d, want 0", op, got)
		}
	}
}

func TestClassifyBranch_RST(t *testing.T) {
	cpu, _ := createTestCPU()
	for _, op := range []byte{0xC7, 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF} {
		if got := classifyCase(cpu, 0x1000, 0x0038, op); got != 5 {
			t.Errorf("RST op=$%02X classified as %d, want 5", op, got)
		}
	}
}

func TestClassifyBranch_EDRet(t *testing.T) {
	cpu, _ := createTestCPU()
	// ED-prefix RETI/RETN — PC jump means RET.
	if got := classifyCase(cpu, 0x1000, 0x8000, 0xED); got != 4 {
		t.Errorf("ED RET classified as %d, want 4", got)
	}
	// ED-prefix instruction with no branch (PC = prevPC + 2 like
	// LD A,I or PC = prevPC + 4 like LD (nn),BC) → no classification.
	if got := classifyCase(cpu, 0x1000, 0x1002, 0xED); got != 0 {
		t.Errorf("ED non-branch +2: classified %d, want 0", got)
	}
	if got := classifyCase(cpu, 0x1000, 0x1004, 0xED); got != 0 {
		t.Errorf("ED non-branch +4: classified %d, want 0", got)
	}
}

func TestClassifyBranch_DDFD_JPIndexed(t *testing.T) {
	cpu, _ := createTestCPU()
	// JP (IX) is DD E9; JP (IY) is FD E9. PC jumped to arbitrary
	// target — classify as 1 (JP). Other DD/FD instructions
	// (LD r,(IX+d), arithmetic etc.) have PC = prevPC + {2,3,4}.
	if got := classifyCase(cpu, 0x1000, 0x9000, 0xDD); got != 1 {
		t.Errorf("DD JP (IX) classified as %d, want 1", got)
	}
	if got := classifyCase(cpu, 0x1000, 0x9000, 0xFD); got != 1 {
		t.Errorf("FD JP (IY) classified as %d, want 1", got)
	}
	for _, delta := range []uint16{2, 3, 4} {
		if got := classifyCase(cpu, 0x1000, 0x1000+delta, 0xDD); got != 0 {
			t.Errorf("DD non-branch +%d: classified %d, want 0", delta, got)
		}
		if got := classifyCase(cpu, 0x1000, 0x1000+delta, 0xFD); got != 0 {
			t.Errorf("FD non-branch +%d: classified %d, want 0", delta, got)
		}
	}
}

func TestClassifyBranch_ResetSourceNotOverwritten(t *testing.T) {
	cpu, _ := createTestCPU()
	// BranchSource = 6/7/8 means NMI / IRQ / Reset already
	// classified the transition. classifyBranch must not stomp on
	// that even if the next instruction would normally re-classify.
	cpu.BranchSource = 8 // SourceReset
	cpu.PC = 0x0000
	cpu.classifyBranch(0xDEAD, 0xC3) // JP would normally set to 1
	if cpu.BranchSource != 8 {
		t.Errorf("classifyBranch overwrote reset source: %d, want 8", cpu.BranchSource)
	}

	cpu.BranchSource = 7 // SourceIRQ
	cpu.PC = 0x0038
	cpu.classifyBranch(0xDEAD, 0xC9) // RET would normally set to 4
	if cpu.BranchSource != 7 {
		t.Errorf("classifyBranch overwrote IRQ source: %d, want 7", cpu.BranchSource)
	}

	cpu.BranchSource = 6 // SourceNMI
	cpu.PC = 0x0066
	cpu.classifyBranch(0xDEAD, 0xCD) // CALL would normally set to 3
	if cpu.BranchSource != 6 {
		t.Errorf("classifyBranch overwrote NMI source: %d, want 6", cpu.BranchSource)
	}
}

func TestClassifyBranch_NonBranchOpcodes(t *testing.T) {
	cpu, _ := createTestCPU()
	// Random non-branch opcodes — no classification at all.
	for _, op := range []byte{0x00, 0x01, 0x76, 0x3E, 0xAF, 0xB7} {
		if got := classifyCase(cpu, 0x1000, 0x1001, op); got != 0 {
			t.Errorf("non-branch op=$%02X classified as %d, want 0", op, got)
		}
	}
}
