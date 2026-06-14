package debugger

import (
	"image"
	"testing"
)

// TestImageViewRenderSwap verifies the panel builds with a nil render
// (blank placeholder) and picks up a new frame + status on SetRender.
func TestImageViewRenderSwap(t *testing.T) {
	v := NewImageView("Layer 2", 100, 100, nil)
	if v.Root() == nil {
		t.Fatal("Root() is nil")
	}

	called := false
	v.SetRender(func() (image.Image, string) {
		called = true
		return image.NewRGBA(image.Rect(0, 0, 256, 192)), "256x192"
	})
	if !called {
		t.Error("render func not invoked on SetRender/Refresh")
	}
	img, st := v.frame()
	if st != "256x192" {
		t.Errorf("status = %q, want 256x192", st)
	}
	if b := img.Bounds(); b.Dx() != 256 || b.Dy() != 192 {
		t.Errorf("frame = %dx%d, want 256x192", b.Dx(), b.Dy())
	}
}
