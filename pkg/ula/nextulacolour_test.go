package ula

import (
	"image/color"
	"testing"
)

// On the Next the ULA's colours come from the Next ULA palette, through the
// index the FPGA forms (pkg/next ULAColour), not the classic fixed palette.

type tagColour struct{}

// Colour encodes its inputs so the test can read back what was asked.
func (tagColour) Colour(attr byte, set, flash, border bool) color.RGBA {
	c := color.RGBA{R: attr, A: 0xFF}
	if set {
		c.G = 1
	}
	if border {
		c.B = 1
	}
	return c
}

func TestNextULATakesItsColoursFromTheColourPath(t *testing.T) {
	u, mem := newFloatingBusULA(t)
	page := mem.GetPage(5)
	for i := 0; i < 0x1800; i++ {
		page[i] = 0
	}
	page[screenAddrForRowCol(0, 0)] = 0x80 // pixel 0 of row 0 set
	for i := 0x1800; i < 0x1B00; i++ {
		page[i] = 0x2A
	}
	u.BorderColour = 3
	u.SetNextULAColour(tagColour{})
	img := u.Render()

	ink := img.RGBAAt(BorderLeft, BorderTop)
	if ink.R != 0x2A || ink.G != 1 || ink.B != 0 {
		t.Errorf("set pixel %v, want the path's ink for attr $2A", ink)
	}
	paper := img.RGBAAt(BorderLeft+1, BorderTop)
	if paper.R != 0x2A || paper.G != 0 || paper.B != 0 {
		t.Errorf("clear pixel %v, want the path's paper for attr $2A", paper)
	}
	border := img.RGBAAt(2, 2)
	if border.R != 3<<3|3 || border.B != 1 {
		t.Errorf("border %v, want the path's border for colour 3", border)
	}
}

// The ULA border is three bits (zxula.vhd:418): a wider stored value must not
// reach the bright bit of the border's attribute.
func TestNextBorderUsesOnlyThreeBits(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	u.SetNextULAColour(tagColour{})
	if got := u.borderRGBA(0x0B); got.R != 3<<3|3 {
		t.Errorf("border 0x0B gave attr %#x, want %#x", got.R, 3<<3|3)
	}
}
