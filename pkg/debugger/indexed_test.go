package debugger

import (
	"image/color"
	"testing"
)

// TestRenderIndexed verifies a palette-indexed framebuffer (one byte
// per pixel, row-major) resolves through colorOf into the right pixels.
// Shared engine behind the Layer-2 framebuffer and tilemap viewers.
func TestRenderIndexed(t *testing.T) {
	rows := [][]byte{
		{0, 1},
		{2, 0},
	}
	pal := map[byte]color.RGBA{
		0: {A: 255},         // black
		1: {R: 255, A: 255}, // red
		2: {G: 255, A: 255}, // green
	}
	img := RenderIndexed(rows, func(b byte) color.RGBA { return pal[b] })
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("bounds = %dx%d, want 2x2", b.Dx(), b.Dy())
	}
	if img.RGBAAt(1, 0) != pal[1] {
		t.Errorf("(1,0) = %v, want red", img.RGBAAt(1, 0))
	}
	if img.RGBAAt(0, 1) != pal[2] {
		t.Errorf("(0,1) = %v, want green", img.RGBAAt(0, 1))
	}
}
