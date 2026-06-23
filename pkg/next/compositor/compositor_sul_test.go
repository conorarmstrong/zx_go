package compositor

import (
	"image/color"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

type fixedPriority struct{ m PriorityMode }

func (f fixedPriority) Mode() PriorityMode { return f.m }

// TestComposeScanlineSULStencilAndFallback verifies the SUL per-pixel
// stencil (Layer 2 shows through a transparent ULA pixel) and the NR$4A
// fallback (a pixel transparent in every layer shows the fallback colour).
func TestComposeScanlineSULStencilAndFallback(t *testing.T) {
	pal := palette.NewBank()
	pal.Select(palette.PaletteLayer2First)
	pal.Active().Set(5, 0b1_1100_0000) // L2 entry 5 = red
	// Transparency is compared against the palette-MAPPED colour: map index 7
	// to colour 7 (identity, as the standard power-on palette does) so it
	// resolves to NR$14 = 7 and is treated transparent.
	pal.Active().Set(7, uint16(7)<<1)
	pal.Select(0)

	// L2 row 0 (256 mode = row-major, pixel x = bank[x]): col 0 = index 5
	// (red, opaque); col 1 = index 7 (== NR$14 → transparent).
	bank := make([]byte, 16384)
	bank[0] = 5
	bank[1] = 7
	l2 := layer2.New(&fakeBanks{banks: map[int][]byte{0: bank}})
	l2.SetActiveBank(0)
	l2.SetEnabled(true)

	c := New(pal, l2)
	c.SetPrioritySource(fixedPriority{ModeSUL})
	var ulaPal [16]color.RGBA
	ulaPal[7] = color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF} // transparency colour
	c.SetULAPalette(ulaPal)
	c.SetTransparency(7) // NR$14 = 7 (< 16 → ULA transparency active)
	c.SetFallbackColour(0x99, 0x88, 0x77)

	// Both pixels: the ULA is the transparency colour (transparent ULA).
	ulaRGBA := make([]byte, Width*4)
	for _, off := range []int{0, 4} {
		ulaRGBA[off+0], ulaRGBA[off+1], ulaRGBA[off+2], ulaRGBA[off+3] = 0x11, 0x22, 0x33, 0xFF
	}
	dst := make([]byte, Width*4)
	c.ComposeScanline(0, ulaRGBA, dst)

	// Pixel 0: transparent ULA + opaque L2 → SUL stencil shows L2 (red).
	if dst[0] == 0 || dst[1] != 0 || dst[2] != 0 {
		t.Errorf("SUL stencil: pixel 0 = %d,%d,%d, want L2 red", dst[0], dst[1], dst[2])
	}
	// Pixel 1: transparent ULA + transparent L2 → NR$4A fallback.
	if dst[4] != 0x99 || dst[5] != 0x88 || dst[6] != 0x77 {
		t.Errorf("NR$4A fallback: pixel 1 = %d,%d,%d, want 153,136,119", dst[4], dst[5], dst[6])
	}
}

// TestComposeScanlineSULLayer2Priority verifies the per-pixel Layer 2
// priority bit promotes L2 above an opaque ULA in SUL mode.
func TestComposeScanlineSULLayer2Priority(t *testing.T) {
	pal := palette.NewBank()
	pal.Select(palette.PaletteLayer2First)
	pal.Active().Set(5, 0b1_1100_0000) // red
	pal.Active().SetPriority(5, 0x02)  // L2 promote bit (NR$44 bit 7)
	pal.Select(0)

	bank := make([]byte, 16384)
	bank[0] = 5
	l2 := layer2.New(&fakeBanks{banks: map[int][]byte{0: bank}})
	l2.SetActiveBank(0)
	l2.SetEnabled(true)

	c := New(pal, l2)
	c.SetPrioritySource(fixedPriority{ModeSUL}) // L2 below ULA
	c.SetTransparency(0xE3)                     // default → opaque ULA

	ulaRGBA := make([]byte, Width*4)
	ulaRGBA[0], ulaRGBA[1], ulaRGBA[2], ulaRGBA[3] = 0x10, 0x20, 0x30, 0xFF
	dst := make([]byte, Width*4)
	c.ComposeScanline(0, ulaRGBA, dst)

	// SUL would normally show the opaque ULA; the priority bit promotes L2.
	if dst[0] == 0 || dst[1] != 0 || dst[2] != 0 {
		t.Errorf("SUL L2 priority: pixel 0 = %d,%d,%d, want L2 red (promoted)", dst[0], dst[1], dst[2])
	}
}
