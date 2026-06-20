package sam

import (
	"image"
	"image/color"
)

// Internal render resolution: the active display, unified at MODE 3's native
// 512-pixel width (lo-res modes 1/2/4 double each pixel). The border is added
// with the GUI hookup. Scaled to 4:3 by the GUI.
const (
	samActiveWidth  = 512
	samActiveHeight = 192
)

const (
	mode12DataBytes    = 0x1800 // 6144 — MODE 1/2 pixel data size
	mode2AttrOffset    = 0x2000 // MODE 2 line-attribute block
	mode34BytesPerLine = 128    // MODE 3/4 bytes per scan line
	flashFramesPhase   = 16     // FLASH toggles every 16 frames
	samCyclesPerLine   = 384    // CPU cycles per scan line
	samTopBorderLines  = 68     // scan lines before the active display
)

// The display is drawn line-accurately: each scan line is rendered with the
// memory + ASIC state (mode, CLUT, border) as of the moment the beam passed it.
// flushRaster (called on every display-memory write and every display-affecting
// OUT, before the change applies) renders the lines the raster has newly passed;
// RunFrame flushes the remainder at frame end. This makes mid-frame raster
// effects (palette/mode splits) correct.

// Render returns the current frame buffer (built line-accurately during
// RunFrame). The GUI calls this once per frame.
func (m *Machine) Render() *image.RGBA { return m.frame }

// renderAll re-renders the whole frame at the current static state (used by the
// renderer unit tests and for a one-shot repaint).
func (m *Machine) renderAll() *image.RGBA {
	m.renderCursor = 0
	m.flushTo(samActiveHeight)
	return m.frame
}

// rasterLine maps the current frame-relative T-state to the active display line
// (0..192). Lines before the top border / after the display clamp to the ends.
func (m *Machine) rasterLine() int {
	rel := m.CPU.Tstates() - m.frameStart
	line := int(rel/samCyclesPerLine) - samTopBorderLines
	if line < 0 {
		return 0
	}
	if line > samActiveHeight {
		return samActiveHeight
	}
	return line
}

// flushRaster renders up to the current raster position; the video-write hook
// and display-affecting OUTs call it before mutating state.
func (m *Machine) flushRaster() { m.flushTo(m.rasterLine()) }

// flushTo renders scan lines [renderCursor, target) and advances the cursor.
func (m *Machine) flushTo(target int) {
	if target > samActiveHeight {
		target = samActiveHeight
	}
	for y := m.renderCursor; y < target; y++ {
		m.renderLine(y)
	}
	if target > m.renderCursor {
		m.renderCursor = target
	}
}

// renderLine draws one scan line in the current screen mode.
func (m *Machine) renderLine(y int) {
	mode := m.Mem.ScreenMode()
	// SOFF blanks the screen in modes 3/4 only (SimCoupe ScreenDisabled).
	if m.border&borderSOFF != 0 && mode >= 3 {
		m.drawBlackLine(y)
		return
	}
	switch mode {
	case 2:
		m.renderMode2Line(y)
	case 3:
		m.renderMode3Line(y)
	case 4:
		m.renderMode4Line(y)
	default:
		m.renderMode1Line(y)
	}
}

func (m *Machine) drawBlackLine(y int) {
	black := color.RGBA{A: 0xFF}
	for x := 0; x < samActiveWidth; x++ {
		m.frame.SetRGBA(x, y, black)
	}
}

// clutColour resolves a 4-bit CLUT index → its master-palette colour.
func (m *Machine) clutColour(index byte) color.RGBA {
	return samPalette[m.clut[index&0x0F]&0x7F]
}

// renderMode1Line: ZX-interleaved 1bpp data + 8×8 paper/ink attributes + FLASH.
func (m *Machine) renderMode1Line(y int) {
	flash := (m.frameCount/flashFramesPhase)&1 == 1
	lineBase := ((y & 0xC0) << 5) | ((y & 0x07) << 8) | ((y & 0x38) << 2)
	attrBase := mode12DataBytes + ((y & 0xF8) << 2)
	m.renderAttrLine(y, lineBase, attrBase, false, flash)
}

// renderMode2Line: linear 1bpp data + a per-line attribute block at 0x2000.
func (m *Machine) renderMode2Line(y int) {
	flash := (m.frameCount/flashFramesPhase)&1 == 1
	lineBase := y << 5
	attrBase := mode2AttrOffset + (y << 5)
	m.renderAttrLine(y, lineBase, attrBase, true, flash)
}

// renderAttrLine draws an attribute-based line (mode 1 or 2): 32 cells of 8
// pixels, each pixel doubled to 512-wide, coloured by per-cell paper/ink. When
// perLineAttr is false the attribute byte is shared across the 8-line char row.
func (m *Machine) renderAttrLine(y, lineBase, attrBase int, perLineAttr, flash bool) {
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
			m.frame.SetRGBA(x, y, c)
			m.frame.SetRGBA(x+1, y, c)
		}
	}
}

// renderMode3Line: 512×192 4-colour hi-res. 128 bytes/line, 2 bits/pixel
// (MSB-pair first), no doubling. Colour is a 4-entry sub-CLUT selected by the
// HMPR mode-3 colour bits, with sub-entries 1 and 2 swapped.
func (m *Machine) renderMode3Line(y int) {
	base := (m.Mem.HMPR() & 0x60) >> 3
	sub := [4]color.RGBA{
		samPalette[m.clut[base]&0x7F],
		samPalette[m.clut[base+2]&0x7F], // swapped
		samPalette[m.clut[base+1]&0x7F], // swapped
		samPalette[m.clut[base+3]&0x7F],
	}
	lineBase := y * mode34BytesPerLine
	for b := 0; b < mode34BytesPerLine; b++ {
		data := m.Mem.VideoMemByte(lineBase + b)
		x := b * 4
		m.frame.SetRGBA(x, y, sub[data>>6])
		m.frame.SetRGBA(x+1, y, sub[(data>>4)&3])
		m.frame.SetRGBA(x+2, y, sub[(data>>2)&3])
		m.frame.SetRGBA(x+3, y, sub[data&3])
	}
}

// renderMode4Line: 256×192 16-colour. 128 bytes/line, two 4-bit CLUT-indexed
// pixels per byte (high nibble first), each doubled to 512-wide.
func (m *Machine) renderMode4Line(y int) {
	lineBase := y * mode34BytesPerLine
	for b := 0; b < mode34BytesPerLine; b++ {
		data := m.Mem.VideoMemByte(lineBase + b)
		hi := m.clutColour(data >> 4)
		lo := m.clutColour(data & 0x0F)
		x := b * 4
		m.frame.SetRGBA(x, y, hi)
		m.frame.SetRGBA(x+1, y, hi)
		m.frame.SetRGBA(x+2, y, lo)
		m.frame.SetRGBA(x+3, y, lo)
	}
}
