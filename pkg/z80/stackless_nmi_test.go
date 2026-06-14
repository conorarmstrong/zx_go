package z80

import "testing"

// The ZX Spectrum Next "stackless NMI" (NextReg $C0 bit 3): on NMI the return
// address is ALSO written to NR$C2 (LSB)/NR$C3 (MSB), and RETN takes its PC
// from NR$C2/$C3 instead of the popped stack value (the stack is still
// pushed/popped so SP stays correct). NextZXOS uses this to launch 128 BASIC:
// it fires the MF NMI, the handler rewrites NR$C2/$C3 to the editor's entry,
// and RETN jumps there. Without it, RETN returns to the interrupted launcher
// and the launch aborts to the welcome screen. Matches a faithful Z80N
// core (the NMI path writes $C2/$C3; RETN reads them back).
func TestStacklessNMI_RETNReturnsToNextReg(t *testing.T) {
	mem := &flatMem{}
	c := New(mem, nullULA{})
	nr := map[byte]byte{}
	c.StacklessReadNR = func(r byte) byte { return nr[r] }
	c.StacklessWriteNR = func(r, v byte) { nr[r] = v }
	c.NMIStackless = true
	c.SP = 0x8000
	c.PC = 0x1234

	c.NMI()
	if c.PC != 0x0066 {
		t.Fatalf("after NMI, PC=%04X want 0066", c.PC)
	}
	if nr[0xC2] != 0x34 || nr[0xC3] != 0x12 {
		t.Fatalf("after NMI, NR$C2/$C3=%02X/%02X want 34/12 (the return PC)", nr[0xC2], nr[0xC3])
	}
	if c.SP != 0x7FFE {
		t.Fatalf("after NMI, SP=%04X want 7FFE (PC still pushed)", c.SP)
	}

	// The handler rewrites NR$C2/$C3 to redirect the return (e.g. the editor).
	nr[0xC2] = 0x78
	nr[0xC3] = 0x56

	mem[0x0066] = 0xED
	mem[0x0067] = 0x45 // RETN
	c.PC = 0x0066
	c.StepInstruction()
	if c.PC != 0x5678 {
		t.Fatalf("stackless RETN took PC=%04X want 5678 (from NR$C2/$C3, not the stack)", c.PC)
	}
	if c.SP != 0x8000 {
		t.Fatalf("after RETN, SP=%04X want 8000 (stack still popped)", c.SP)
	}
}

// Regression: with stackless DISABLED, NMI/RETN use the stack as normal.
func TestStacklessNMI_DisabledUsesStack(t *testing.T) {
	mem := &flatMem{}
	c := New(mem, nullULA{})
	c.NMIStackless = false
	c.SP = 0x8000
	c.PC = 0x1234

	c.NMI()
	mem[0x0066] = 0xED
	mem[0x0067] = 0x45 // RETN
	c.PC = 0x0066
	c.StepInstruction()
	if c.PC != 0x1234 {
		t.Fatalf("non-stackless RETN PC=%04X want 1234 (from the stack)", c.PC)
	}
}
