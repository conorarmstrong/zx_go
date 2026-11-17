package next

import (
	"image/color"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// ULAColour is the Next ULA's colour path. The ULA forms an 8-bit index into
// the ULA palette from the attribute byte, the pixel bit and the mode
// (zxula.vhd:483-560), and the colour is that entry of the active ULA palette
// (NR$43 bit 1). In ULANext a paper pixel the format leaves no room for takes
// the NR$4A fallback colour instead (ula_select_bgnd, zxnext.vhd:6987-6990).
//
// The modes:
//
//	standard  ink   000 0 B iii    paper 000 1 B ppp    (flash swaps them)
//	ULANext   ink   attr AND NR$42 paper 1... attr>>n  (NR$43 bit 0)
//	ULA+      ink   11 gg 0 iii    paper 11 gg 1 ppp    (NR$68 bit 3, not with ULANext)
//
// The border is a paper pixel of attribute 00bbbbbb (zxula.vhd:418), with its
// own ULANext rule: 1000 0bbb.
type ULAColour struct {
	d   *nextregs.Dispatcher
	pal *palette.Bank
}

// NewULAColour builds the colour path over the register file and the palette.
func NewULAColour(d *nextregs.Dispatcher, pal *palette.Bank) *ULAColour {
	return &ULAColour{d: d, pal: pal}
}

// Colour returns the colour of a ULA pixel: attr is the attribute byte (for
// the border, b<<3|b), set the pixel bit, flash the flash phase, border
// whether the pixel is border.
func (u *ULAColour) Colour(attr byte, set, flash, border bool) color.RGBA {
	ulanext := u.d.Raw(0x43)&0x01 != 0
	ulaplus := u.d.Raw(0x68)&0x08 != 0 && !ulanext
	var idx byte
	switch {
	case ulanext:
		format := u.d.Raw(0x42)
		switch {
		case border:
			if format == 0xFF {
				return u.fallback()
			}
			idx = 0x80 | (attr>>3)&0x07
		case set:
			idx = attr & format
		default:
			n := paperShift(format)
			if n < 0 {
				return u.fallback()
			}
			idx = 0x80 | attr>>uint(n)
		}
	case ulaplus:
		idx = 0xC0 | (attr>>6)<<4
		if set && !border {
			idx |= attr & 0x07
		} else {
			idx |= 0x08 | (attr>>3)&0x07
		}
	default:
		if attr&0x80 != 0 && flash && !border {
			set = !set
		}
		idx = (attr & 0x40) >> 3 // bright
		if set && !border {
			idx |= attr & 0x07
		} else {
			idx |= 0x10 | (attr>>3)&0x07
		}
	}
	p := u.pal.PaletteForLayer(palette.LayerULA)
	if p == nil {
		return color.RGBA{A: 0xFF}
	}
	r, g, b := p.RGB(idx)
	return color.RGBA{R: r, G: g, B: b, A: 0xFF}
}

// paperShift returns how far the attribute is shifted to form a ULANext paper
// index for an ink mask format (zxula.vhd:516-526), or -1 when the format has
// no paper field.
func paperShift(format byte) int {
	switch format {
	case 0x01:
		return 1
	case 0x03:
		return 2
	case 0x07:
		return 3
	case 0x0F:
		return 4
	case 0x1F:
		return 5
	case 0x3F:
		return 6
	case 0x7F:
		return 7
	}
	return -1
}

// fallback is the NR$4A colour, its 9th bit the OR of the two blue bits
// (zxnext.vhd:6990).
func (u *ULAColour) fallback() color.RGBA {
	v := u.d.Raw(0x4A)
	b3 := (v&0x03)<<1 | (v>>1|v)&0x01
	return color.RGBA{R: x3(v >> 5), G: x3(v >> 2 & 0x07), B: x3(b3), A: 0xFF}
}

func x3(v byte) byte { v &= 0x07; return v<<5 | v<<2 | v>>1 }
