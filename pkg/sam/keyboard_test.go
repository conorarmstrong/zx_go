package sam

import "testing"

func TestKeyboardReleasedReadsHigh(t *testing.T) {
	k := NewKeyboard()
	if got := k.Scan(0x00); got != 0xFF { // all rows selected, none pressed
		t.Errorf("no keys pressed should read 0xFF, got %#02x", got)
	}
}

func TestKeyboardRowSelect(t *testing.T) {
	k := NewKeyboard()
	k.SetKey(3, 0, true) // row 3, column 0 (the '1' key)
	// Selecting row 3 (clear bit 3 of high byte) should show column 0 low.
	if got := k.Scan(^byte(1 << 3)); got&0x01 != 0 {
		t.Errorf("row 3 col 0 should read low when row 3 selected, got %#02x", got)
	}
	// Selecting only row 2 must NOT show it.
	if got := k.Scan(^byte(1 << 2)); got&0x01 == 0 {
		t.Errorf("row 3 key must not appear when only row 2 is selected, got %#02x", got)
	}
}

func TestKeyboardColumnSplit(t *testing.T) {
	k := NewKeyboard()
	k.SetKey(6, 6, true) // row 6 column 6 (COLON) — a high column (status-port side)
	got := k.Scan(^byte(1 << 6))
	if got&0x40 != 0 {
		t.Errorf("column 6 should read low, got %#02x", got)
	}
	if got&0x1F != 0x1F {
		t.Errorf("low columns should stay high, got %#02x", got)
	}
}

func TestKeyboardRow8OnlyOnFF(t *testing.T) {
	k := NewKeyboard()
	k.SetKey(8, 0, true) // CONTROL
	if got := k.Scan(0xFF); got&0x01 != 0 {
		t.Errorf("row 8 should read on high byte 0xFF, got %#02x", got)
	}
	if got := k.Scan(0x00); got&0x01 == 0 {
		t.Errorf("row 8 must not leak into a normal scan, got %#02x", got)
	}
}

func TestKeyboardReleaseAll(t *testing.T) {
	k := NewKeyboard()
	k.SetKey(0, 0, true)
	k.SetKey(7, 1, true)
	k.ReleaseAll()
	if got := k.Scan(0x00); got != 0xFF {
		t.Errorf("ReleaseAll should clear the matrix, got %#02x", got)
	}
}
