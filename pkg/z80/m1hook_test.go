package z80

import "testing"

// The ZX80/ZX81 generate their display by the ULA forcing the CPU to execute a
// NOP while it fetches a character bitmap. M1FetchHook models that: it sees the
// fetch address and the byte read, and returns the opcode actually executed.
func TestM1FetchHookSubstitutesOpcode(t *testing.T) {
	cpu, mem := createTestCPU()
	mem.Write(0x8000, 0x3C) // INC A
	cpu.PC = 0x8000
	cpu.A = 0x10

	var seenPC uint16
	var seenOp byte
	cpu.M1FetchHook = func(pc uint16, op byte) byte {
		seenPC, seenOp = pc, op
		if pc >= 0x8000 { // emulate a ZX81 video fetch: substitute NOP
			return 0x00
		}
		return op
	}

	cpu.executeInstruction()

	if seenPC != 0x8000 || seenOp != 0x3C {
		t.Errorf("hook saw pc=%04X op=%02X, want 8000 3C", seenPC, seenOp)
	}
	if cpu.A != 0x10 {
		t.Errorf("A=%02X — INC A executed; NOP substitution failed", cpu.A)
	}
	if cpu.PC != 0x8001 {
		t.Errorf("PC=%04X, want 8001 (NOP advances one byte)", cpu.PC)
	}
}
