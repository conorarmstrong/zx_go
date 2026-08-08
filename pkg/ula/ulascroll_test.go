package ula

import (
	"image/color"
	"testing"
)

// NextReg 0x26 / 0x27 are the ULA's own horizontal and vertical scroll. They
// were entirely unimplemented, which is also why NR$68 bit 2 (the half-pixel
// refinement of the horizontal scroll) had nothing to attach to.
//
// Vertical wrap is not a plain modulo: the screen is 192 lines, so
// zxula.vhd:200-206 folds the 9-bit sum back with
//
//	py_s = vc + scroll_y
//	py_s[8:7] == "11"                  -> py = (not py_s[7]) & py_s[6:0]
//	py_s[8] or py_s[7:6] == "11"       -> py = (py_s[7:6] + 1) & py_s[5:0]
//	otherwise                          -> py = py_s[7:0]

// TestULAScrollRowIdentityWithNoScroll pins the no-op case: every display row
// maps to itself.
func TestULAScrollRowIdentityWithNoScroll(t *testing.T) {
	for vc := 0; vc < ScreenHeight; vc++ {
		if got := ulaScrollRow(vc, 0); got != vc {
			t.Fatalf("ulaScrollRow(%d, 0) = %d, want %d", vc, got, vc)
		}
	}
}

// TestULAScrollRowMatchesTheFPGAFold pins the wrap against the VHDL formula,
// evaluated independently here.
func TestULAScrollRowMatchesTheFPGAFold(t *testing.T) {
	fold := func(vc int, scroll byte) int {
		p := vc + int(scroll)
		b8, b7, b6 := (p>>8)&1, (p>>7)&1, (p>>6)&1
		switch {
		case b8 == 1 && b7 == 1:
			return p & 0x7F // bit 7 forced low
		case b8 == 1 || (b7 == 1 && b6 == 1):
			return ((((p >> 6) & 3) + 1) & 3 << 6) | (p & 0x3F)
		default:
			return p & 0xFF
		}
	}
	for _, scroll := range []byte{0, 1, 7, 8, 63, 64, 100, 191, 192, 200, 255} {
		for vc := 0; vc < ScreenHeight; vc++ {
			if got, want := ulaScrollRow(vc, scroll), fold(vc, scroll); got != want {
				t.Fatalf("ulaScrollRow(%d, %d) = %d, want %d", vc, scroll, got, want)
			}
		}
	}
}

// TestULAScrollRowStaysOnScreen pins the point of the fold: whatever the
// scroll, every row still lands inside the 192-line screen.
func TestULAScrollRowStaysOnScreen(t *testing.T) {
	for scroll := 0; scroll < 256; scroll++ {
		for vc := 0; vc < ScreenHeight; vc++ {
			if got := ulaScrollRow(vc, byte(scroll)); got < 0 || got >= ScreenHeight {
				t.Fatalf("ulaScrollRow(%d, %d) = %d, outside 0..191", vc, scroll, got)
			}
		}
	}
}

// TestULAScrollRowShiftsByWholeLines pins the straightforward case: a scroll
// well inside the screen just moves the source row down by that much.
func TestULAScrollRowShiftsByWholeLines(t *testing.T) {
	for _, scroll := range []byte{1, 10, 50} {
		for vc := 0; vc+int(scroll) < 192; vc++ {
			if got, want := ulaScrollRow(vc, scroll), vc+int(scroll); got != want {
				t.Fatalf("ulaScrollRow(%d, %d) = %d, want %d", vc, scroll, got, want)
			}
		}
	}
}

// TestULAScrollColumnSplitsCharacterAndPixel pins the horizontal decode:
// NR$26 bits 7:3 are a whole-character offset and bits 2:0 a pixel shift
// within the character (zxula.vhd:199).
func TestULAScrollColumnSplitsCharacterAndPixel(t *testing.T) {
	for _, tc := range []struct {
		scroll   byte
		wantChar int
		wantPix  int
	}{
		{0x00, 0, 0},
		{0x01, 0, 1},
		{0x07, 0, 7},
		{0x08, 1, 0},
		{0x09, 1, 1},
		{0xFF, 31, 7},
	} {
		gotChar, gotPix := ulaScrollColumn(tc.scroll)
		if gotChar != tc.wantChar || gotPix != tc.wantPix {
			t.Errorf("ulaScrollColumn(%#02x) = char %d pixel %d, want char %d pixel %d",
				tc.scroll, gotChar, gotPix, tc.wantChar, tc.wantPix)
		}
	}
}

// TestULAScrollMovesTheRenderedPicture pins the end-to-end effect. The marker
// is a single all-ink row (or column) against an otherwise blank screen, so
// the assertion cannot be satisfied by coincidence the way a
// "byte(row)" pattern can — rows 0 and 8 there differ only in a low bit,
// leaving the top-left pixel identical.
func TestULAScrollMovesTheRenderedPicture(t *testing.T) {
	// markRow paints one screen row all-ink, everything else blank.
	markRow := func(t *testing.T, row int) *ULA {
		t.Helper()
		u, mem := newFloatingBusULA(t)
		page := mem.GetPage(5)
		for i := 0; i < 0x1800; i++ {
			page[i] = 0x00
		}
		for col := 0; col < 32; col++ {
			page[screenAddrForRowCol(row, col)] = 0xFF
		}
		for i := 0x1800; i < 0x1B00; i++ {
			page[i] = 0x07 // white ink, black paper
		}
		return u
	}
	inkAt := func(img interface{ RGBAAt(x, y int) color.RGBA }, x, y int) bool {
		c := img.RGBAAt(BorderLeft+x, BorderTop+y)
		return c.R > 0x80 && c.G > 0x80 && c.B > 0x80
	}

	// Source row 8 is the only lit row. Unscrolled it appears at row 8.
	u := markRow(t, 8)
	base := u.Render()
	if inkAt(base, 0, 0) {
		t.Fatal("precondition: display row 0 should be blank")
	}
	if !inkAt(base, 0, 8) {
		t.Fatal("precondition: display row 8 should be lit")
	}

	// With a vertical scroll of 8, source row 8 is fetched for display row 0.
	u2 := markRow(t, 8)
	u2.SetULAScrollY(8)
	scrolled := u2.Render()
	if !inkAt(scrolled, 0, 0) {
		t.Error("NR$27 = 8: display row 0 is blank, want source row 8 (lit)")
	}
	if inkAt(scrolled, 0, 8) {
		t.Error("NR$27 = 8: display row 8 is still lit, so nothing moved")
	}

	// Horizontal: light one character column and scroll a whole character.
	markCol := func(t *testing.T, col int) *ULA {
		t.Helper()
		u, mem := newFloatingBusULA(t)
		page := mem.GetPage(5)
		for i := 0; i < 0x1800; i++ {
			page[i] = 0x00
		}
		for row := 0; row < 192; row++ {
			page[screenAddrForRowCol(row, col)] = 0xFF
		}
		for i := 0x1800; i < 0x1B00; i++ {
			page[i] = 0x07
		}
		return u
	}
	h := markCol(t, 1)
	if inkAt(h.Render(), 0, 0) {
		t.Fatal("precondition: column 0 should be blank")
	}
	h2 := markCol(t, 1)
	h2.SetULAScrollX(8) // one whole character
	if !inkAt(h2.Render(), 0, 0) {
		t.Error("NR$26 = 8: pixel column 0 is blank, want character column 1 (lit)")
	}
}

// TestULAScrollDefaultsAreNoOp guards every classic model: with both
// registers at their reset value the render must be byte-identical to the
// unscrolled path.
func TestULAScrollDefaultsAreNoOp(t *testing.T) {
	u, mem := newFloatingBusULA(t)
	page := mem.GetPage(5)
	for i := range page {
		page[i] = byte(i)
	}
	first := u.Render()
	before := make([]byte, len(first.Pix))
	copy(before, first.Pix)

	u.SetULAScrollX(0)
	u.SetULAScrollY(0)
	after := u.Render()
	for i := range before {
		if before[i] != after.Pix[i] {
			t.Fatalf("zero scroll changed the frame at byte %d", i)
		}
	}
}
