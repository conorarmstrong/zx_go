package debugger

import (
	"image/color"
	"testing"
)

// TestRenderSpriteSheet verifies sprites (each a 16×16 RGBA block,
// row-major) tile into a cols-wide sheet, scaled by `scale`, and that
// a known sprite pixel lands in the right place at the right colour.
func TestRenderSpriteSheet(t *testing.T) {
	// One sprite: top-left pixel red, everything else transparent-black.
	var sp [SpritePixels]color.RGBA
	sp[0] = color.RGBA{R: 255, A: 255}
	sheet := RenderSpriteSheet([][SpritePixels]color.RGBA{sp}, 2, 4)

	// scale 2 → 32×32 cell. Pixel (0,0)..(1,1) should be red.
	if got := sheet.RGBAAt(0, 0); got != (color.RGBA{R: 255, A: 255}) {
		t.Errorf("(0,0) = %v, want red", got)
	}
	if got := sheet.RGBAAt(1, 1); got != (color.RGBA{R: 255, A: 255}) {
		t.Errorf("(1,1) = %v, want red (scaled 2x)", got)
	}
	// Pixel (2,0) is the next source pixel (index 1), which is zero.
	if got := sheet.RGBAAt(2, 0); got != (color.RGBA{}) {
		t.Errorf("(2,0) = %v, want zero", got)
	}
}
