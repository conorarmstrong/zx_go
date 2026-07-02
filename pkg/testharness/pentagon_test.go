package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The Pentagon 128 must cold-boot its editor ROM to the (non-blank) 128-style
// menu, exercising the ModelPentagon memory path end-to-end.
func TestPentagonBoots(t *testing.T) {
	h, err := New(roms.ModelPentagon)
	if err != nil {
		t.Fatalf("New(ModelPentagon): %v", err)
	}
	h.RunFrames(200)

	img := h.ULA().Render()
	b := img.Bounds()
	first := img.At(b.Min.X, b.Min.Y)
	distinct := false
	for y := b.Min.Y; y < b.Max.Y && !distinct; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.At(x, y) != first {
				distinct = true
				break
			}
		}
	}
	if !distinct {
		t.Error("Pentagon screen is a single flat colour after 200 frames — did not boot/render")
	}
	t.Logf("Pentagon PC=%04X", h.CPU().PC)
}
