package sam

import "testing"

func TestFrameIncludesBorder(t *testing.T) {
	m := New(make([]byte, PageSize), make([]byte, PageSize))
	b := m.Render().Bounds()
	if b.Dx() != samTotalWidth || b.Dy() != samTotalHeight {
		t.Fatalf("frame is %dx%d, want %dx%d (active + border)", b.Dx(), b.Dy(), samTotalWidth, samTotalHeight)
	}
}

func TestBorderShowsBorderColour(t *testing.T) {
	m := New(make([]byte, PageSize), make([]byte, PageSize))
	// Point CLUT entry 2 at blue, then select border colour index 2 (BORDER
	// byte 0x02 → BORDER_COLOUR = 2).
	m.WritePort(0x02F8, 0x10) // CLUT[2] = 0x10 (pure blue)
	m.WritePort(0x00FE, 0x02) // BORDER colour index 2
	m.renderAll()

	want := PaletteColour(0x10)
	corners := [][2]int{
		{0, 0},                                  // top-left border
		{samTotalWidth - 1, 0},                  // top-right border
		{0, samTotalHeight - 1},                 // bottom-left border
		{samBorderLeft - 1, samTotalHeight / 2}, // left side margin
	}
	for _, p := range corners {
		if got := m.frame.RGBAAt(p[0], p[1]); got != want {
			t.Errorf("border pixel (%d,%d) = %v, want blue %v", p[0], p[1], got, want)
		}
	}
}

// TestBorderColourIndex covers the BORDER → CLUT-index decode (low 3 bits + bit 5).
func TestBorderColourIndex(t *testing.T) {
	cases := map[byte]byte{
		0x00: 0,  // colour 0
		0x07: 7,  // low three bits
		0x08: 0,  // bit 3 is MIC, not colour
		0x20: 8,  // bit 5 → index bit 3
		0x27: 15, // all colour bits
		0x3F: 15, // MIC/BEEP set too, colour still 15
	}
	for border, want := range cases {
		if got := borderColourIndex(border); got != want {
			t.Errorf("borderColourIndex(%#02x) = %d, want %d", border, got, want)
		}
	}
}
