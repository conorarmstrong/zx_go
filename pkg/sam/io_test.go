package sam

import "testing"

// newTestMachine builds a machine on the synthetic ROM and advances one frame so
// CPU.Tstates() is large enough for frame-relative arithmetic without underflow.
func newTestMachine(t *testing.T) *Machine {
	t.Helper()
	m, err := NewFromROM(synthROM())
	if err != nil {
		t.Fatal(err)
	}
	m.RunFrame()
	return m
}

// setFrameRel positions the machine so statusByte sees the given frame-relative
// T-state.
func setFrameRel(m *Machine, rel uint64) { m.frameStart = m.CPU.Tstates() - rel }

func TestPagingPortRouting(t *testing.T) {
	m := newTestMachine(t)
	m.WritePort(0x00FA, 0x05) // LMPR
	m.WritePort(0x00FB, 0x06) // HMPR
	m.WritePort(0x00FC, 0x42) // VMPR
	m.WritePort(0x0080, 0x11) // LEPR
	m.WritePort(0x0081, 0x22) // HEPR
	if m.Mem.LMPR() != 0x05 || m.Mem.HMPR() != 0x06 {
		t.Errorf("LMPR/HMPR routing: got %#02x/%#02x", m.Mem.LMPR(), m.Mem.HMPR())
	}
	if m.Mem.LEPR() != 0x11 || m.Mem.HEPR() != 0x22 {
		t.Errorf("LEPR/HEPR routing: got %#02x/%#02x", m.Mem.LEPR(), m.Mem.HEPR())
	}
	// VMPR readback ORs in RXMIDI.
	if v, _ := m.ReadPort(0x00FC); v != (0x42 | 0x80) {
		t.Errorf("VMPR readback = %#02x, want %#02x", v, 0x42|0x80)
	}
	if v, _ := m.ReadPort(0x00FA); v != 0x05 {
		t.Errorf("LMPR readback = %#02x, want 0x05", v)
	}
}

func TestBorderPort(t *testing.T) {
	m := newTestMachine(t)
	m.WritePort(0x00FE, 0x27) // colour bits 0-2 + bit5 = full colour index 15
	if m.BorderColour() != 0x0F {
		t.Errorf("BorderColour = %#x, want 0x0F", m.BorderColour())
	}
	// SOFF (bit 7) is reflected in the keyboard-port read (bit 7).
	m.WritePort(0x00FE, borderSOFF)
	if v, _ := m.ReadPort(0xFFFE); v&borderSOFF == 0 {
		t.Errorf("SOFF should read back high on the keyboard port, got %#02x", v)
	}
}

func TestCLUTWrite(t *testing.T) {
	m := newTestMachine(t)
	m.WritePort(0x05F8, 0xC2) // CLUT index 5 (high byte) = 0xC2 & 0x7F
	if got := m.CLUT()[5]; got != 0x42 {
		t.Errorf("CLUT[5] = %#02x, want 0x42 (7-bit masked)", got)
	}
}

func TestStatusFrameInterrupt(t *testing.T) {
	m := newTestMachine(t)
	// Inside the frame-INT window → frame bit (0x08) clear (pending, active-low).
	setFrameRel(m, samFrameIntTstate+10)
	if m.statusByte()&statusFrame != 0 {
		t.Errorf("frame bit should be clear (pending) inside the window, got %#02x", m.statusByte())
	}
	// Outside the window → frame bit set (not pending).
	setFrameRel(m, samFrameIntTstate+samIntActiveCycles+50)
	if m.statusByte()&statusFrame == 0 {
		t.Errorf("frame bit should be set (not pending) outside the window, got %#02x", m.statusByte())
	}
}

func TestStatusLineInterrupt(t *testing.T) {
	m := newTestMachine(t)
	m.setLineInterrupt(100) // enable line INT at scan line 100
	if m.CPU.LineIntOffsetTstates != uint64((100+68)*384) {
		t.Errorf("line INT offset = %d, want %d", m.CPU.LineIntOffsetTstates, (100+68)*384)
	}
	lt := uint64((100 + 68) * 384)
	setFrameRel(m, lt+5)
	if m.statusByte()&statusLine != 0 {
		t.Errorf("line bit should be clear (pending) in window, got %#02x", m.statusByte())
	}
	// Disabled (line >= 192): never pending, and the Z80 line hook is cleared.
	m.setLineInterrupt(0xFF)
	if m.CPU.LineIntOffsetTstates != 0 {
		t.Errorf("disabling the line INT should clear the offset, got %d", m.CPU.LineIntOffsetTstates)
	}
	setFrameRel(m, lt+5)
	if m.statusByte()&statusLine == 0 {
		t.Errorf("line bit should stay set when disabled, got %#02x", m.statusByte())
	}
}

func TestKeyboardPortRead(t *testing.T) {
	m := newTestMachine(t)
	m.Kbd.SetKey(3, 0, true) // row 3 col 0 (low column → keyboard port)
	m.Kbd.SetKey(6, 6, true) // row 6 col 6 (high column → status port)

	// Keyboard port 0xFE, select row 3: column 0 reads low; EAR (0x40) high.
	kbAddr := uint16(^byte(1<<3))<<8 | 0xFE
	v, _ := m.ReadPort(kbAddr)
	if v&0x01 != 0 {
		t.Errorf("row 3 col 0 should read low on keyboard port, got %#02x", v)
	}
	if v&keyboardEAR == 0 {
		t.Errorf("EAR bit should read high, got %#02x", v)
	}
	// Status port 0xF9, select row 6: column 6 (0x40) reads low.
	stAddr := uint16(^byte(1<<6))<<8 | 0xF9
	v, _ = m.ReadPort(stAddr)
	if v&0x40 != 0 {
		t.Errorf("row 6 col 6 should read low on status port, got %#02x", v)
	}
}
