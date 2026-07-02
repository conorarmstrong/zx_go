package keyboard

import (
	"testing"

	"fyne.io/fyne/v2"
)

// scannedBits reports the matrix bits seen through the port-scan interface
// (so it reflects the typed-character symbol pulse, which Scan overlays). It
// scans each row individually via its address line.
func scannedBits(k *Keyboard) map[[2]byte]bool {
	out := map[[2]byte]bool{}
	for row := 0; row < 8; row++ {
		addrHi := byte(^(1 << uint(row))) // active-low: only this row selected
		b := k.Scan(uint16(addrHi) << 8)
		for bit := byte(0x01); bit <= 0x10; bit <<= 1 {
			if b&bit == 0 {
				out[[2]byte{byte(row), bit}] = true
			}
		}
	}
	return out
}

// TestTypedRuneSymbols pins the layout-independent symbol mappings. The ';'
// case is easy to transpose with '<'/'>' (SYMBOL SHIFT + W/T): ';' must map
// to SYMBOL SHIFT + O.
func TestTypedRuneSymbols(t *testing.T) {
	sym := [2]byte{7, 0x02} // SYMBOL SHIFT
	cases := []struct {
		r    rune
		key  [2]byte
		desc string
	}{
		{'.', [2]byte{7, 0x04}, ". = SYMBOL SHIFT + M"},
		{',', [2]byte{7, 0x08}, ", = SYMBOL SHIFT + N"},
		{';', [2]byte{5, 0x02}, "; = SYMBOL SHIFT + O"},
		{':', [2]byte{0, 0x02}, ": = SYMBOL SHIFT + Z"},
		{'?', [2]byte{0, 0x08}, "? = SYMBOL SHIFT + C"},
		{'"', [2]byte{5, 0x01}, "\" = SYMBOL SHIFT + P"},
		{'=', [2]byte{6, 0x02}, "= = SYMBOL SHIFT + L"},
		{'/', [2]byte{0, 0x10}, "/ = SYMBOL SHIFT + V"},
		{'!', [2]byte{3, 0x01}, "! = SYMBOL SHIFT + 1"},
		{'(', [2]byte{4, 0x04}, "( = SYMBOL SHIFT + 8"},
	}
	for _, c := range cases {
		k := New()
		if !k.TypeRune(c.r) {
			t.Errorf("%s: TypeRune(%q) returned false", c.desc, c.r)
			continue
		}
		got := scannedBits(k)
		if !got[sym] {
			t.Errorf("%s: SYMBOL SHIFT not pressed (got %v)", c.desc, got)
		}
		if !got[c.key] {
			t.Errorf("%s: target key row=%d mask=0x%02x not pressed (got %v)", c.desc, c.key[0], c.key[1], got)
		}
		if len(got) != 2 {
			t.Errorf("%s: expected exactly SYM+key, got %v", c.desc, got)
		}
	}
}

// TestTypedRuneNonSymbolIgnored confirms letters/digits are NOT consumed by the
// rune path (they're held via the physical key path so games keep working).
func TestTypedRuneNonSymbolIgnored(t *testing.T) {
	k := New()
	for _, r := range []rune{'a', 'Z', '1', '0', ' '} {
		if k.TypeRune(r) {
			t.Errorf("TypeRune(%q) should return false (handled by the physical key path)", r)
		}
	}
}

// TestTypedRunePulseReleases confirms the injected symbol is held briefly then
// released by Tick().
func TestTypedRunePulseReleases(t *testing.T) {
	k := New()
	k.TypeRune('.')
	if len(scannedBits(k)) == 0 {
		t.Fatal("symbol should be pressed immediately after TypeRune")
	}
	for i := 0; i < runePulseFrames; i++ {
		k.Tick()
	}
	if n := len(scannedBits(k)); n != 0 {
		t.Errorf("symbol should be released after %d ticks, still %d bits pressed", runePulseFrames, n)
	}
}

// TestTypedRuneSuppressesCapsShift covers the French-AZERTY case: '.' is
// Shift+';'-key, so the host Shift sets CAPS SHIFT — which must NOT leak into
// the injected symbol (CAPS+SYM = extended mode → wrong character).
func TestTypedRuneSuppressesCapsShift(t *testing.T) {
	k := New()
	// Host Shift held → CAPS SHIFT active (keyName is irrelevant here).
	k.HandleKeyWithModifiers(fyne.KeyUnknown, true, true, false, false, false)
	k.TypeRune('.')
	got := scannedBits(k)
	if got[[2]byte{0, 0x01}] {
		t.Error("CAPS SHIFT must be suppressed during a symbol pulse")
	}
	if !got[[2]byte{7, 0x02}] || !got[[2]byte{7, 0x04}] {
		t.Errorf("'.' must scan as SYMBOL SHIFT + M, got %v", got)
	}
}
