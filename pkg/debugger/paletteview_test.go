package debugger

import (
	"image/color"
	"testing"
)

// TestPaletteViewRendersProvider verifies the widget builds a
// 16×16-cell image from its provider and survives a nil provider +
// a provider swap without panicking.
func TestPaletteViewRendersProvider(t *testing.T) {
	pv := NewPaletteView(nil) // nil → all-black, must not panic
	if pv.Root() == nil {
		t.Fatal("Root() is nil")
	}
	want := 16 * paletteCell
	if b := pv.renderImage().Bounds(); b.Dx() != want || b.Dy() != want {
		t.Fatalf("image = %dx%d, want %dx%d", b.Dx(), b.Dy(), want, want)
	}

	// Swap in a provider whose entry 0 is a known colour and confirm
	// the rendered top-left pixel matches.
	pv.SetProvider(func() [256]color.RGBA {
		var c [256]color.RGBA
		c[0] = color.RGBA{R: 0xAB, G: 0xCD, B: 0xEF, A: 0xFF}
		return c
	})
	img := pv.renderImage()
	got, ok := img.At(0, 0).(color.RGBA)
	if !ok {
		// image.RGBA.At returns color.RGBA via the model; normalise.
		r, g, b, a := img.At(0, 0).RGBA()
		got = color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
	}
	if got != (color.RGBA{R: 0xAB, G: 0xCD, B: 0xEF, A: 0xFF}) {
		t.Errorf("entry 0 pixel = %v, want #ABCDEF", got)
	}
}
