package sam

import "image/color"

// samPalette is the SAM Coupé 128-colour master palette. Each 7-bit index maps
// to three 3-bit channels with a shared low bit (the SAM's GGG/RRR/BBB + common
// LSB layout), scaled linearly 0-7 → 0-255.
//
//	red   = i.bit5·4 + i.bit1·2 + i.bit3   (bits {5,1,3})
//	green = i.bit6·4 + i.bit2·2 + i.bit3   (bits {6,2,3})
//	blue  = i.bit4·4 + i.bit0·2 + i.bit3   (bits {4,0,3})
var samPalette [128]color.RGBA

func init() {
	scale := func(c3 int) uint8 { return uint8(c3 * 255 / 7) }
	for i := 0; i < 128; i++ {
		r3 := (i & 0x02) | ((i & 0x20) >> 3) | ((i & 0x08) >> 3)
		g3 := ((i & 0x04) >> 1) | ((i & 0x40) >> 4) | ((i & 0x08) >> 3)
		b3 := ((i & 0x01) << 1) | ((i & 0x10) >> 2) | ((i & 0x08) >> 3)
		samPalette[i] = color.RGBA{R: scale(r3), G: scale(g3), B: scale(b3), A: 0xFF}
	}
}

// PaletteColour returns the RGBA for a 7-bit master-palette index.
func PaletteColour(index byte) color.RGBA { return samPalette[index&0x7F] }
