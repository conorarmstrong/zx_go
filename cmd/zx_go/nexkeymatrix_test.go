package main

import "testing"

// The typist reaches $, = and % with SYMBOL SHIFT + 4, L and 5, as
// printed on the Spectrum keyboard; NextBASIC strings and integer variables need them.
func TestNexKeyMatrixTypesDollarEqualsAndPercent(t *testing.T) {
	sym := [2]int{7, 0x02}
	for r, want := range map[rune][2]int{'$': {3, 0x08}, '=': {6, 0x02}, '%': {3, 0x10}} {
		k, ok := nexKeyMatrix[r]
		if !ok || len(k) != 2 || k[0] != sym || k[1] != want {
			t.Errorf("%q: got %v, want SYMBOL SHIFT + %v", r, k, want)
		}
	}
}
