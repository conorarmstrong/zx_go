package copper

import "testing"

// The FPGA's nr_copper_addr is an 11-bit BYTE address, 0..$7FF
// (zxnext.vhd:1194). NR$60 writes one byte at that address and steps it
// by one (5417-5424); NR$61 sets bits 7:0 (5427); NR$62 sets the mode in
// bits 7:6 and address bits 10:8 from ITS bits 2:0 (5430-5431).
//
// We modelled the cursor as a 10-bit INSTRUCTION index and masked NR$62
// with two bits, so half the copper RAM could not be addressed, an
// address written to NR$61 landed at twice its intended word, and a
// read-back of NR$61 after two NR$60 writes returned 1 instead of 2.
func TestCursorIsAByteAddress(t *testing.T) {
	c := New()
	if got := c.Cursor(); got != 0 {
		t.Fatalf("fresh cursor = %d, want 0", got)
	}
	c.WriteData(0x12)
	if got := c.Cursor(); got != 1 {
		t.Errorf("cursor after one byte = %d, want 1", got)
	}
	c.WriteData(0x34)
	if got := c.Cursor(); got != 2 {
		t.Errorf("cursor after two bytes = %d, want 2", got)
	}
}

// NR$62 carries three address bits, so the whole 2 KB is reachable.
func TestCursorHighBitsAreThreeBitsWide(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x07) // addr(10:8) = 111, mode 00
	c.SetWritePtrLow(0xFF)
	if got := c.Cursor(); got != 0x7FF {
		t.Errorf("cursor = %#03x, want 0x7ff (the top of copper RAM)", got)
	}
}

// A byte written at an even address is the instruction's high half; the
// next byte is its low half.
func TestBytesLandInTheirOwnHalfOfTheWord(t *testing.T) {
	c := New()
	c.SetWritePtrLow(4) // byte address 4 = word 2, high half
	c.WriteData(0x81)
	c.WriteData(0x23)
	if got := c.program[2]; got != 0x8123 {
		t.Errorf("word 2 = %#04x, want 0x8123", got)
	}
}

// Starting at an ODD address writes only the low half, leaving the high
// half alone. Our two-byte staging paired every write with the next one
// instead, so a list uploaded from an odd address came out shifted.
func TestAnOddAddressWritesOnlyTheLowHalf(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	c.WriteData(0xAA) // word 0 high
	c.WriteData(0xBB) // word 0 low
	c.SetWritePtrLow(1)
	c.WriteData(0x55) // word 0 low only
	if got := c.program[0]; got != 0xAA55 {
		t.Errorf("word 0 = %#04x, want 0xaa55", got)
	}
}

// The cursor wraps at the end of copper RAM.
func TestCursorWrapsAtTheTopOfCopperRAM(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x07)
	c.SetWritePtrLow(0xFF)
	c.WriteData(0x00)
	if got := c.Cursor(); got != 0 {
		t.Errorf("cursor after the last byte = %#03x, want 0", got)
	}
}

// copper_list_addr_s is reset only when the mode CHANGES to 01 or 11
// (device/copper.vhd:70-76: `if last_state_s /= copper_en_i`). Writing
// the same mode again — which guest code does whenever it sets the
// cursor high bits — must not restart the program.
func TestWritingTheSameModeDoesNotRestartTheProgram(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x40) // mode 01: start from zero
	c.pc = 7
	c.SetWritePtrHighAndMode(0x40) // same mode, e.g. while moving the cursor
	if got := c.pc; got != 7 {
		t.Errorf("PC = %d after re-writing the same mode, want 7", got)
	}
}

func TestChangingTheModeToStartFromZeroRestartsTheProgram(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0xC0) // mode 11
	c.pc = 7
	c.SetWritePtrHighAndMode(0x40) // mode 01: a real change
	if got := c.pc; got != 0 {
		t.Errorf("PC = %d after a mode change to 01, want 0", got)
	}
}
