package keyboard

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// ReleaseAll is what focus loss and reboot call. It used to clear only
// the physical matrix, leaving TypeRune's SYMBOL-SHIFT overlay live for
// up to two more frames: BASIC then typed a run of the same symbol
// after every key had been let go.
func TestReleaseAllDropsTheTypedSymbolOverlay(t *testing.T) {
	k := New()
	k.TypeRune('.')
	if k.Scan(0x7FFE) == 0xFF && k.Scan(0xBFFE) == 0xFF {
		t.Fatal("setup: TypeRune did not assert anything")
	}

	k.ReleaseAll()

	for _, addr := range []uint16{0xFEFE, 0xFDFE, 0xFBFE, 0xF7FE, 0xEFFE, 0xDFFE, 0xBFFE, 0x7FFE} {
		if got := k.Scan(addr); got != 0xFF {
			t.Errorf("row %#04x: got %#02x after ReleaseAll, want 0xFF", addr, got)
		}
	}
}

// F11 is BREAK, which is CAPS SHIFT + SPACE. Releasing it used to OR
// both bits back on unconditionally, so a physically-held Shift or
// Space was lifted out of the matrix with it and stayed gone until the
// user released and pressed the key again.
func TestBreakReleaseKeepsAHeldShiftDown(t *testing.T) {
	k := New()
	k.HandleKeyEvent(&fyne.KeyEvent{Name: desktop.KeyShiftLeft}, true)
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, true)
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, false)

	// Row 0xFE is CAPS SHIFT's row; bit 0 is CAPS SHIFT itself.
	if got := k.Scan(0xFEFE); got&0x01 != 0 {
		t.Errorf("CAPS SHIFT: row reads %#02x, want bit 0 clear (still held)", got)
	}
}

func TestBreakReleaseKeepsAHeldSpaceDown(t *testing.T) {
	k := New()
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeySpace}, true)
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, true)
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, false)

	// Row 0x7F is SPACE's row; bit 0 is SPACE itself.
	if got := k.Scan(0x7FFE); got&0x01 != 0 {
		t.Errorf("SPACE: row reads %#02x, want bit 0 clear (still held)", got)
	}
}

// And the converse: with nothing else held, releasing BREAK must
// actually lift both keys.
func TestBreakReleaseLiftsBothWhenNothingElseIsHeld(t *testing.T) {
	k := New()
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, true)
	k.HandleKeyEvent(&fyne.KeyEvent{Name: fyne.KeyF11}, false)

	if got := k.Scan(0xFEFE); got != 0xFF {
		t.Errorf("CAPS SHIFT row: got %#02x, want 0xFF", got)
	}
	if got := k.Scan(0x7FFE); got != 0xFF {
		t.Errorf("SPACE row: got %#02x, want 0xFF", got)
	}
}
