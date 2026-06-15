package zx8x

import (
	"image"
	"testing"
)

// The ZX80/ZX81 generate the picture by having the ULA fetch a character's
// bitmap from the character-generator ROM: the low 6 bits of a display byte
// select the character, and each character is 8 bytes (one per scanline) shifted
// out MSB-first (bit 7 = leftmost pixel, a set bit = ink/black).
func TestCharGenRowReturnsCharacterScanline(t *testing.T) {
	// Synthetic character generator: character 1, scanline 0, has the leftmost
	// and rightmost pixels set.
	charset := make([]byte, 64*8)
	charset[1*8+0] = 0b1000_0001

	g := NewCharGen(charset)

	got := g.Row(0x01, 0) // display byte: character code 1, scanline 0
	if got != 0b1000_0001 {
		t.Errorf("Row(0x01, 0) = %08b, want %08b", got, 0b1000_0001)
	}
}

// Bit 7 of a display byte selects inverse (reverse) video: every pixel of the
// character's scanline is flipped.
func TestCharGenRowInverseVideoFlipsPixels(t *testing.T) {
	charset := make([]byte, 64*8)
	charset[1*8+0] = 0b1000_0001

	g := NewCharGen(charset)

	got := g.Row(0x81, 0) // character code 1 (0x01) | inverse bit (0x80)
	if got != 0b0111_1110 {
		t.Errorf("Row(0x81, 0) = %08b, want %08b", got, 0b0111_1110)
	}
}

// A Screen renders display bytes into the 256×192 (32×24 cell) ZX8x picture.
// Plotting a character fills its 8×8 cell; pixel bit 7 of each scanline is the
// leftmost pixel.
func TestScreenPlotsCharacterCellIntoFramebuffer(t *testing.T) {
	charset := make([]byte, 64*8)
	charset[1*8+0] = 0b1000_0001 // character 1, scanline 0: leftmost + rightmost ink
	s := NewScreen(NewCharGen(charset))

	s.PlotChar(0, 0, 0x01) // character 1 at top-left cell

	if !s.PixelAt(0, 0) {
		t.Error("pixel (0,0) should be ink")
	}
	if !s.PixelAt(7, 0) {
		t.Error("pixel (7,0) should be ink")
	}
	for x := 1; x <= 6; x++ {
		if s.PixelAt(x, 0) {
			t.Errorf("pixel (%d,0) should be paper", x)
		}
	}
}

// The display file is a stream of character bytes with HALT ($76) terminating
// each line (and a leading HALT before line 0). Characters plot left-to-right;
// each HALT advances to the next text row.
func TestScreenRendersDisplayFileWithNewlines(t *testing.T) {
	charset := make([]byte, 64*8)
	charset[1*8+0] = 0b1000_0000 // character 1, scanline 0: leftmost pixel only
	charset[2*8+0] = 0b0000_0001 // character 2, scanline 0: rightmost pixel only
	s := NewScreen(NewCharGen(charset))

	// leading HALT, line 0 = char 1, HALT, line 1 = char 2, HALT
	s.RenderDisplayFile([]byte{0x76, 0x01, 0x76, 0x02, 0x76})

	if !s.PixelAt(0, 0) {
		t.Error("line 0 character should plot at cell (0,0) — leftmost pixel of (0,0)")
	}
	if !s.PixelAt(7, 8) {
		t.Error("line 1 character should plot at cell (0,1) — rightmost pixel at y=8")
	}
	if s.PixelAt(7, 0) {
		t.Error("(7,0) should be paper — line 1's character must not appear on line 0")
	}
}

// Image renders the monochrome ZX8x picture: ink is black, paper is white, at
// the full 256×192 resolution.
func TestScreenImageInkBlackPaperWhite(t *testing.T) {
	charset := make([]byte, 64*8)
	charset[1*8+0] = 0x80 // character 1, scanline 0: leftmost pixel ink
	s := NewScreen(NewCharGen(charset))
	s.PlotChar(0, 0, 0x01)

	img := s.Image()
	if img.Bounds() != image.Rect(0, 0, TotalWidth, TotalHeight) {
		t.Fatalf("bounds = %v, want 0,0,%d,%d", img.Bounds(), TotalWidth, TotalHeight)
	}
	// Cell (0,0) sits at the top-left of the active area, inside the border.
	if r, g, b, _ := img.At(BorderLeft, BorderTop).RGBA(); r != 0 || g != 0 || b != 0 {
		t.Errorf("ink pixel at active (0,0) = %d,%d,%d, want black", r, g, b)
	}
	if r, _, _, _ := img.At(BorderLeft+1, BorderTop).RGBA(); r == 0 {
		t.Errorf("paper pixel at active (1,0) should be white, got r=%d", r)
	}
	// The border itself is white.
	if r, _, _, _ := img.At(0, 0).RGBA(); r == 0 {
		t.Errorf("border pixel (0,0) should be white, got r=%d", r)
	}
}
