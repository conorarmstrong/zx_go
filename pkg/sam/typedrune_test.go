package sam

import "testing"

// selectRow returns the IN high byte that selects exactly one matrix row.
func selectRow(row int) byte { return ^(byte(1) << uint(row)) }

func TestTypeRuneDedicatedKey(t *testing.T) {
	k := NewKeyboard()
	if !k.TypeRune('"') {
		t.Fatal(`TypeRune('"') returned false; should be a known symbol`)
	}
	// QUOTES is row 5, column 6 (read on port 0xF9). It must read pressed.
	if got := k.Scan(selectRow(5)); got&0x40 != 0 {
		t.Errorf(`after TypeRune('"'), row5 scan = %#02x, QUOTES (0x40) not pressed`, got)
	}
}

func TestTypeRunePulseReleases(t *testing.T) {
	k := NewKeyboard()
	k.TypeRune('"')
	// Tick past the pulse; the key must release.
	for i := 0; i < samRunePulseFrames+1; i++ {
		k.Tick()
	}
	if got := k.Scan(selectRow(5)); got&0x40 == 0 {
		t.Errorf("pulse did not release: row5 scan = %#02x still shows QUOTES pressed", got)
	}
}

func TestTypeRuneShiftCombo(t *testing.T) {
	k := NewKeyboard()
	k.TypeRune('(') // SHIFT + 8
	// SHIFT is row0 col0 (port 0xFE bit0).
	if got := k.Scan(selectRow(0)); got&0x01 != 0 {
		t.Errorf("'(' did not hold SHIFT: row0 scan = %#02x", got)
	}
	// '8' is row4 col2 (port 0xFE bit2).
	if got := k.Scan(selectRow(4)); got&0x04 != 0 {
		t.Errorf("'(' did not press the 8 key: row4 scan = %#02x", got)
	}
}

func TestTypeRuneSymbolCombo(t *testing.T) {
	k := NewKeyboard()
	k.TypeRune('<') // SYMBOL + COMMA
	// SYMBOL is row7 col1 (port 0xFE bit1).
	if got := k.Scan(selectRow(7)); got&0x02 != 0 {
		t.Errorf("'<' did not hold SYMBOL: row7 scan = %#02x", got)
	}
	// COMMA is row7 col5 (port 0xF9 bit5).
	if got := k.Scan(selectRow(7)); got&0x20 != 0 {
		t.Errorf("'<' did not press COMMA: row7 scan = %#02x", got)
	}
}

func TestTypeRuneLetterIsIgnored(t *testing.T) {
	k := NewKeyboard()
	if k.TypeRune('A') {
		t.Error("TypeRune('A') should return false — letters use the physical key path")
	}
}
