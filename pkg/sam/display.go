package sam

import (
	"image"
	"image/color"
)

// Internal render resolution: the active display, unified at MODE 3's native
// 512-pixel width (lo-res modes 1/2/4 double each pixel). The border is added
// in Sprint 4 with the line-accurate renderer. Scaled to 4:3 by the GUI.
const (
	samActiveWidth  = 512
	samActiveHeight = 192
)

const (
	mode12DataBytes    = 0x1800 // 6144 — MODE 1/2 pixel data size
	mode34BytesPerLine = 128    // MODE 3/4 bytes per scan line
	flashFramesPhase   = 16     // FLASH toggles every 16 frames
)

// Render draws the current SAM frame into a 512×192 RGBA image. MODE 1 and
// MODE 4 are implemented (Sprint 3); MODE 2 and MODE 3 land in Sprint 4 and
// currently fall back to MODE 1 / MODE 4 respectively so the buffer is never
// blank.
func (m *Machine) Render() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, samActiveWidth, samActiveHeight))
	switch m.Mem.ScreenMode() {
	case 4, 3:
		m.renderMode4(img)
	default: // 1, 2
		m.renderMode1(img)
	}
	return img
}

// clutColour resolves a 4-bit CLUT index → its master-palette colour.
func (m *Machine) clutColour(index byte) color.RGBA {
	return samPalette[m.clut[index&0x0F]&0x7F]
}

// renderMode1 draws the Spectrum-compatible mode: 32×192 cells of 1bpp pixel
// data in ZX-interleaved line order, with 8×8 attribute cells (paper/ink/FLASH)
// after the 6144-byte bitmap. Each source pixel is doubled to 512-wide.
func (m *Machine) renderMode1(img *image.RGBA) {
	flash := (m.frameCount/flashFramesPhase)&1 == 1
	for y := 0; y < samActiveHeight; y++ {
		lineBase := ((y & 0xC0) << 5) | ((y & 0x07) << 8) | ((y & 0x38) << 2)
		attrBase := mode12DataBytes + ((y & 0xF8) << 2)
		for cell := 0; cell < 32; cell++ {
			data := m.Mem.VideoMemByte(lineBase + cell)
			attr := m.Mem.VideoMemByte(attrBase + cell)
			paper := (attr >> 3) & 0x0F
			ink := (attr & 0x07) | ((attr >> 3) & 0x08)
			if flash && attr&0x80 != 0 {
				ink, paper = paper, ink
			}
			inkC := m.clutColour(ink)
			paperC := m.clutColour(paper)
			for bit := 0; bit < 8; bit++ {
				c := paperC
				if data&(0x80>>bit) != 0 {
					c = inkC
				}
				x := (cell*8 + bit) * 2
				img.SetRGBA(x, y, c)
				img.SetRGBA(x+1, y, c)
			}
		}
	}
}

// renderMode4 draws the 256×192 16-colour mode: 128 bytes/line, two 4-bit
// pixels per byte (high nibble first), each doubled to 512-wide. Colour is the
// CLUT entry indexed directly by the nibble.
func (m *Machine) renderMode4(img *image.RGBA) {
	for y := 0; y < samActiveHeight; y++ {
		lineBase := y * mode34BytesPerLine
		for b := 0; b < mode34BytesPerLine; b++ {
			data := m.Mem.VideoMemByte(lineBase + b)
			hi := m.clutColour(data >> 4)
			lo := m.clutColour(data & 0x0F)
			x := b * 4
			img.SetRGBA(x, y, hi)
			img.SetRGBA(x+1, y, hi)
			img.SetRGBA(x+2, y, lo)
			img.SetRGBA(x+3, y, lo)
		}
	}
}
