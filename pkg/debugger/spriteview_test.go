package debugger

import (
	"image/color"
	"testing"
)

// TestSpriteViewRendersProvider checks the widget builds, survives a
// nil provider (placeholder), and renders a sheet sized to the sprite
// count from a provider.
func TestSpriteViewRendersProvider(t *testing.T) {
	sv := NewSpriteView(nil)
	if sv.Root() == nil {
		t.Fatal("Root() is nil")
	}
	// nil provider → small placeholder, no panic.
	if b := sv.renderImage().Bounds(); b.Dx() != SpriteDim || b.Dy() != SpriteDim {
		t.Fatalf("placeholder = %dx%d, want %dx%d", b.Dx(), b.Dy(), SpriteDim, SpriteDim)
	}

	// Provider with 2 sprites → sheet is one row (cols=8) of 16*scale.
	sv.SetProvider(func() []SpriteSnapshot {
		var a, b SpriteSnapshot
		a.Index = 0
		a.Pixels[0] = color.RGBA{R: 255, A: 255}
		b.Index = 1
		return []SpriteSnapshot{a, b}
	})
	img := sv.renderImage()
	wantW := spriteCols * SpriteDim * spriteScale
	wantH := SpriteDim * spriteScale
	if g := img.Bounds(); g.Dx() != wantW || g.Dy() != wantH {
		t.Fatalf("sheet = %dx%d, want %dx%d", g.Dx(), g.Dy(), wantW, wantH)
	}
}
