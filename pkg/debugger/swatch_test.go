package debugger

import (
	"image/color"
	"testing"
)

// TestRenderSwatch verifies the colour-swatch grid renderer lays
// entries out left-to-right, top-to-bottom in cell-sized blocks and
// fills each block with that entry's colour. This is the pixel engine
// behind the visual palette / sprite viewers (jnext-parity, graphical).
func TestRenderSwatch(t *testing.T) {
	colours := []color.RGBA{
		{R: 255, A: 255},                 // 0 red
		{G: 255, A: 255},                 // 1 green
		{B: 255, A: 255},                 // 2 blue
		{R: 255, G: 255, B: 255, A: 255}, // 3 white
	}
	img := RenderSwatch(func(i int) color.RGBA { return colours[i] }, len(colours), 4, 2)

	// 2 cols × 2 rows of 4px cells → 8×8 image.
	if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 8 {
		t.Fatalf("bounds = %dx%d, want 8x8", b.Dx(), b.Dy())
	}
	// Sample the centre of each cell.
	cases := []struct {
		x, y int
		want color.RGBA
	}{
		{1, 1, colours[0]}, // top-left
		{5, 1, colours[1]}, // top-right
		{1, 5, colours[2]}, // bottom-left
		{5, 5, colours[3]}, // bottom-right
	}
	for _, c := range cases {
		got := img.RGBAAt(c.x, c.y)
		if got != c.want {
			t.Errorf("pixel (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}
