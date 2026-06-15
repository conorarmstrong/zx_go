package dma

import "testing"

// After a transfer, setting the read mask to the byte-counter registers and
// reading port 0x6B returns the live counter low then high byte.
func TestReadbackByteCounter(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 0x0140, addrIncrement, addrIncrement, true))
	// Read mask: bits 1,2 = byte counter low + high (0x06).
	feed(d, []byte{0xBB, 0x06})
	if lo := d.ReadCommand(); lo != 0x40 {
		t.Errorf("counter low = $%02X, want $40", lo)
	}
	if hi := d.ReadCommand(); hi != 0x01 {
		t.Errorf("counter high = $%02X, want $01", hi)
	}
	// Sequence wraps back to the first masked register.
	if lo := d.ReadCommand(); lo != 0x40 {
		t.Errorf("after wrap, counter low = $%02X, want $40", lo)
	}
}

// The read mask can select the port A / port B address registers, returning
// the live internal pointers.
func TestReadbackPortAddresses(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	// Mask bits 3,4 = port A addr low/high; 5,6 = port B addr low/high (0x78).
	feed(d, []byte{0xBB, 0x78})
	got := []byte{d.ReadCommand(), d.ReadCommand(), d.ReadCommand(), d.ReadCommand()}
	// After a 4-byte incrementing transfer: curA=$4004, curB=$6004.
	want := []byte{0x04, 0x40, 0x04, 0x60}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("readback[%d] = $%02X, want $%02X", i, got[i], want[i])
		}
	}
}

// $A7 (initiate read sequence) resets the read cursor to the first masked
// register.
func TestInitiateReadSequenceResetsCursor(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 0x0102, addrIncrement, addrIncrement, true))
	feed(d, []byte{0xBB, 0x06}) // counter low, high
	_ = d.ReadCommand()         // consume low
	d.WriteCommand(0xA7)        // re-initiate read sequence
	if lo := d.ReadCommand(); lo != 0x02 {
		t.Errorf("after $A7, first read = $%02X, want counter low $02", lo)
	}
}

// The power-up read mask is 0x7F (all seven registers in the read sequence).
func TestDefaultReadMaskAllRegisters(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	// No explicit read mask set → default 0x7F. Seven distinct reads then wrap.
	first := make([]byte, 7)
	for i := range first {
		first[i] = d.ReadCommand()
	}
	if d.ReadCommand() != first[0] {
		t.Error("read sequence did not wrap after 7 registers (default mask 0x7F)")
	}
}
