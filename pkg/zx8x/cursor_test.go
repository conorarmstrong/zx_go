package zx8x

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Regression: the ZX81 boot cursor must render as a single 8×8 character cell,
// not smeared across columns. A bug where the column was reset on a T-state
// boundary instead of the real HALT scanline boundary scattered the cursor's 8
// pixel rows across the full width (x=0..255).
func TestZX81CursorIsSingleCell(t *testing.T) {
	m, _ := NewMachine(roms.ModelZX81, "")
	for i := 0; i < 250; i++ {
		m.RunFrame()
	}
	img := m.Image()
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	ink := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r == 0 {
				ink++
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if ink == 0 {
		t.Fatal("screen is blank — no cursor rendered")
	}
	if maxX-minX > 7 || maxY-minY > 7 {
		t.Errorf("cursor ink spans %d×%d px at x=%d..%d y=%d..%d — expected a single 8×8 cell (column-drift regression?)",
			maxX-minX+1, maxY-minY+1, minX, maxX, minY, maxY)
	}
}
