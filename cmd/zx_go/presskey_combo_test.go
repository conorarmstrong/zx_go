package main

import "testing"

// BREAK = CAPS+SPACE pressed together; quote (") = SYM+P. --press-key
// needs chords: `caps+space@100` must emit one entry per key at the
// same frame so both are held (and released) together by the
// scheduler.
func TestParsePressKeySpecCombo(t *testing.T) {
	got := parsePressKeySpec("caps+space@100")
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	for i, want := range []string{"caps", "space"} {
		if got[i].name != want || got[i].frame != 100 {
			t.Errorf("entry %d = %q@%d, want %q@100", i, got[i].name, got[i].frame, want)
		}
	}
	// chord members map to their own matrix positions
	if got[0].row != 0 || got[0].mask != 0x01 {
		t.Errorf("caps = row %d mask %#02x, want row 0 mask 0x01", got[0].row, got[0].mask)
	}
	if got[1].row != 7 || got[1].mask != 0x01 {
		t.Errorf("space = row %d mask %#02x, want row 7 mask 0x01", got[1].row, got[1].mask)
	}
}

// singles still work alongside chords in one spec
func TestParsePressKeySpecComboMixed(t *testing.T) {
	got := parsePressKeySpec("p@5,sym+p@9")
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3", len(got))
	}
}
