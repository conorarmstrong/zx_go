package keyboard

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// On the ZX80/ZX81 the full-stop key is matrix row 7, bit 1 (there is no
// SYMBOL SHIFT). Scanning row 7 (address high byte with bit 7 = 0) must show
// that bit low when "." is pressed, with SPACE (bit 0) still high.
func TestZX8xLayoutPeriodIsRow7Bit1(t *testing.T) {
	k := New()
	k.UseZX8xLayout()

	k.HandleKeyWithModifiers(fyne.KeyPeriod, true, false, false, false, false)

	row7 := k.Scan(0x7FFF) // A15 low selects row 7
	if row7&0x02 != 0 {
		t.Errorf("'.' pressed: row7 bit1 should be low, got %08b", row7)
	}
	if row7&0x01 == 0 {
		t.Errorf("SPACE (row7 bit0) must not be pressed, got %08b", row7)
	}
}

// NEWLINE/ENTER is matrix row 6, bit 0.
func TestZX8xLayoutNewlineIsRow6Bit0(t *testing.T) {
	k := New()
	k.UseZX8xLayout()

	k.HandleKeyWithModifiers(fyne.KeyReturn, true, false, false, false, false)

	row6 := k.Scan(0xBFFF) // A14 low selects row 6
	if row6&0x01 != 0 {
		t.Errorf("NEWLINE pressed: row6 bit0 should be low, got %08b", row6)
	}
}

// The full ZX80/ZX81 8×5 matrix: every physical key must map to exactly its
// matrix position, with no spurious SHIFT, so the whole keyboard is usable.
func TestZX8xLayoutCompleteMatrix(t *testing.T) {
	type kp struct {
		key  fyne.KeyName
		row  int
		mask byte
	}
	keys := []kp{
		// Row 0: (SHIFT), Z, X, C, V
		{fyne.KeyZ, 0, 0x02}, {fyne.KeyX, 0, 0x04}, {fyne.KeyC, 0, 0x08}, {fyne.KeyV, 0, 0x10},
		// Row 1: A, S, D, F, G
		{fyne.KeyA, 1, 0x01}, {fyne.KeyS, 1, 0x02}, {fyne.KeyD, 1, 0x04}, {fyne.KeyF, 1, 0x08}, {fyne.KeyG, 1, 0x10},
		// Row 2: Q, W, E, R, T
		{fyne.KeyQ, 2, 0x01}, {fyne.KeyW, 2, 0x02}, {fyne.KeyE, 2, 0x04}, {fyne.KeyR, 2, 0x08}, {fyne.KeyT, 2, 0x10},
		// Row 3: 1-5
		{fyne.Key1, 3, 0x01}, {fyne.Key2, 3, 0x02}, {fyne.Key3, 3, 0x04}, {fyne.Key4, 3, 0x08}, {fyne.Key5, 3, 0x10},
		// Row 4: 0, 9, 8, 7, 6
		{fyne.Key0, 4, 0x01}, {fyne.Key9, 4, 0x02}, {fyne.Key8, 4, 0x04}, {fyne.Key7, 4, 0x08}, {fyne.Key6, 4, 0x10},
		// Row 5: P, O, I, U, Y
		{fyne.KeyP, 5, 0x01}, {fyne.KeyO, 5, 0x02}, {fyne.KeyI, 5, 0x04}, {fyne.KeyU, 5, 0x08}, {fyne.KeyY, 5, 0x10},
		// Row 6: NEWLINE, L, K, J, H
		{fyne.KeyReturn, 6, 0x01}, {fyne.KeyL, 6, 0x02}, {fyne.KeyK, 6, 0x04}, {fyne.KeyJ, 6, 0x08}, {fyne.KeyH, 6, 0x10},
		// Row 7: SPACE, ., M, N, B
		{fyne.KeySpace, 7, 0x01}, {fyne.KeyPeriod, 7, 0x02}, {fyne.KeyM, 7, 0x04}, {fyne.KeyN, 7, 0x08}, {fyne.KeyB, 7, 0x10},
	}
	for _, c := range keys {
		k := New()
		k.UseZX8xLayout()
		k.HandleKeyWithModifiers(c.key, true, false, false, false, false)
		if !rowBitLow(k, c.row, c.mask) {
			t.Errorf("key %q: expected matrix row %d mask %#02x pressed", c.key, c.row, c.mask)
		}
		// Plain keys must not assert SHIFT (row 0 bit 0).
		if rowBitLow(k, 0, 0x01) {
			t.Errorf("key %q: must not press SHIFT", c.key)
		}
	}
	// SHIFT itself.
	k := New()
	k.UseZX8xLayout()
	if got := k.GetMapping(desktop.KeyShiftLeft); len(got) != 1 || got[0].Row != 0 || got[0].Mask != 0x01 {
		t.Errorf("SHIFT (ShiftLeft) mapping = %+v, want row0 mask0x01", got)
	}
}

// rowBitLow reports whether matrix (row, mask) reads as pressed (active-low).
func rowBitLow(k *Keyboard, row int, mask byte) bool {
	addr := uint16(byte(^(1 << uint(row)))) << 8 // only `row` selected
	return k.Scan(addr)&mask == 0
}

// The ZX80/ZX81 have no separate symbol/operator keys: every symbol is SHIFT +
// a letter/digit. The host keys for those symbols (and the editing/RUBOUT key)
// must therefore map to the right SHIFT-combination so programs can be typed.
func TestZX8xLayoutSymbolAndEditKeys(t *testing.T) {
	// row/mask of the matrix positions referenced below.
	const (
		shiftRow, shiftMask = 0, 0x01 // SHIFT
	)
	cases := []struct {
		name string
		key  fyne.KeyName
		row  int
		mask byte
	}{
		{"RUBOUT=Shift+0", fyne.KeyBackspace, 4, 0x01},  // 0 = row4 bit0
		{"= is Shift+L", fyne.KeyEqual, 6, 0x02},        // L
		{"- is Shift+J", fyne.KeyMinus, 6, 0x08},        // J
		{", is Shift+N", fyne.KeyComma, 7, 0x08},        // N
		{"; is Shift+X", fyne.KeySemicolon, 0, 0x04},    // X
		{"/ is Shift+V", fyne.KeySlash, 0, 0x10},        // V
		{"( is Shift+I", fyne.KeyLeftBracket, 5, 0x04},  // I
		{") is Shift+O", fyne.KeyRightBracket, 5, 0x02}, // O
		{"+ is Shift+K", "+", 6, 0x04},                  // K (typed rune)
		{": is Shift+Z", ":", 0, 0x02},                  // Z (typed rune)
		{"\" is Shift+P", "\"", 5, 0x01},                // P (typed rune)
		{"* is Shift+B", "*", 7, 0x10},                  // B (typed rune)
	}
	for _, c := range cases {
		k := New()
		k.UseZX8xLayout()
		k.HandleKeyWithModifiers(c.key, true, false, false, false, false)
		if !rowBitLow(k, c.row, c.mask) {
			t.Errorf("%s: target key bit not pressed", c.name)
		}
		if !rowBitLow(k, shiftRow, shiftMask) {
			t.Errorf("%s: SHIFT not pressed", c.name)
		}
	}
}
