package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// The ULA's colour path, zxula.vhd:483-560 and zxnext.vhd:6987-6990: the ULA
// forms an 8-bit index into the ULA palette from the attribute, the pixel bit
// and the mode, and the colour is that palette entry.

func ulaColourRig() (*ULAColour, *nextregs.Dispatcher, *palette.Bank) {
	d := nextregs.New()
	b := palette.NewBank()
	// Give every ULA palette entry a distinct colour: entry i = i (9-bit).
	p := b.PaletteForLayer(palette.LayerULA)
	for i := 0; i < 256; i++ {
		p.Set(byte(i), uint16(i)<<1)
	}
	return NewULAColour(d, b), d, b
}

func wantEntry(t *testing.T, name string, c *ULAColour, b *palette.Bank, attr byte, set, flash, border bool, idx byte) {
	t.Helper()
	got := c.Colour(attr, set, flash, border)
	r, g, bl := b.PaletteForLayer(palette.LayerULA).RGB(idx)
	if got.R != r || got.G != g || got.B != bl {
		t.Errorf("%s: colour %v, want ULA palette entry %d", name, got, idx)
	}
}

func TestULAColourStandardMode(t *testing.T) {
	c, _, b := ulaColourRig()
	// attr: flash 0, bright 1, paper 5, ink 2.
	attr := byte(0x40 | 5<<3 | 2)
	wantEntry(t, "ink", c, b, attr, true, false, false, 8+2)
	wantEntry(t, "paper", c, b, attr, false, false, false, 16+8+5)
	// Flash swaps ink and paper while the flash phase is on.
	wantEntry(t, "flashed ink", c, b, attr|0x80, true, true, false, 16+8+5)
	wantEntry(t, "border", c, b, 3<<3|3, false, false, true, 16+3)
}

func TestULAColourULANext(t *testing.T) {
	c, d, b := ulaColourRig()
	d.WriteReg(0x43, 0x01) // ULANext on
	d.WriteReg(0x42, 0x0F) // ink in bits 3:0, paper in 7:4
	attr := byte(0xA7)
	wantEntry(t, "ink", c, b, attr, true, false, false, 0x07)
	wantEntry(t, "paper", c, b, attr, false, false, false, 0x80|0x0A)
	// Flash is not applied in ULANext.
	wantEntry(t, "no flash", c, b, attr, true, true, false, 0x07)
	wantEntry(t, "border", c, b, 6<<3|6, false, false, true, 0x80|6)
}

func TestULAColourULANextFFPaperIsTheFallback(t *testing.T) {
	c, d, _ := ulaColourRig()
	d.WriteReg(0x43, 0x01)
	d.WriteReg(0x42, 0xFF)
	d.WriteReg(0x4A, 0xE3) // fallback RGB332
	got := c.Colour(0x12, false, false, false)
	if got.R != expand3(7) || got.G != 0 || got.B != expand3(7) {
		t.Errorf("paper with format $FF = %v, want the NR$4A fallback", got)
	}
}

func TestULAColourULAPlus(t *testing.T) {
	c, d, b := ulaColourRig()
	d.WriteReg(0x68, 0x08)               // ULA+ on
	attr := byte(0x80 | 0x40 | 3<<3 | 6) // group 3, paper 3, ink 6
	wantEntry(t, "ink", c, b, attr, true, false, false, 0xC0|3<<4|6)
	wantEntry(t, "paper", c, b, attr, false, false, false, 0xC0|3<<4|1<<3|3)
}

func expand3(v byte) byte { return v<<5 | v<<2 | v>>1 }
