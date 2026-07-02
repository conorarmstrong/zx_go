// Package zx8x implements the shared video generation for the Sinclair ZX80
// and ZX81. Unlike the Spectrum, these machines have no frame-buffer ULA: the
// picture is produced by the CPU fetching display bytes whose low 6 bits index
// a character bitmap in the character-generator ROM, which the ULA shifts out
// pixel by pixel. This package decodes that mechanism into a pixel image.
package zx8x

import (
	"image"
	"image/color"
)

// Picture dimensions: the ZX80/ZX81 display is 32×24 characters of 8×8 pixels,
// shown inside a white border. The bordered frame matches the Spectrum's
// 320×240 so the GUI window scaling is identical across machines.
const (
	Cols   = 32
	Rows   = 24
	Width  = Cols * 8 // 256 — active display width
	Height = Rows * 8 // 192 — active display height

	BorderLeft  = 32                   // left/right border (matches the Spectrum)
	BorderTop   = 24                   // top/bottom border
	TotalWidth  = Width + BorderLeft*2 // 320
	TotalHeight = Height + BorderTop*2 // 240
)

// Screen renders ZX80/ZX81 display bytes into a 256×192 monochrome picture.
type Screen struct {
	cg  *CharGen
	pix []bool // Width*Height, true = ink (black)
}

// NewScreen returns an all-paper Screen that decodes characters via cg.
func NewScreen(cg *CharGen) *Screen {
	return &Screen{cg: cg, pix: make([]bool, Width*Height)}
}

// PlotChar draws display byte b into the character cell at column col (0..31)
// and text row trow (0..23).
func (s *Screen) PlotChar(col, trow int, b byte) {
	for sl := 0; sl < 8; sl++ {
		bits := s.cg.Row(b, sl)
		y := trow*8 + sl
		for px := 0; px < 8; px++ {
			if bits&(0x80>>px) != 0 {
				s.pix[y*Width+col*8+px] = true
			}
		}
	}
}

// PixelAt reports whether the pixel at (x, y) is ink.
func (s *Screen) PixelAt(x, y int) bool {
	return s.pix[y*Width+x]
}

// SetScanlineByte sets the eight horizontal pixels at character column col and
// pixel row y from bits (MSB = leftmost, set = ink). This is the per-scanline
// primitive the live CPU-driven video uses, as opposed to PlotChar which fills
// a whole 8×8 cell. Out-of-range positions are ignored.
func (s *Screen) SetScanlineByte(col, y int, bits byte) {
	if y < 0 || y >= Height || col < 0 || col >= Cols {
		return
	}
	for px := 0; px < 8; px++ {
		if bits&(0x80>>px) != 0 {
			s.pix[y*Width+col*8+px] = true
		}
	}
}

// Clear resets the picture to all paper.
func (s *Screen) Clear() {
	for i := range s.pix {
		s.pix[i] = false
	}
}

// Image renders the picture as a 320×240 RGBA bitmap: the 256×192 active
// display (ink = black, paper = white) centred in a white border, matching the
// real ZX8x white overscan.
func (s *Screen) Image() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, TotalWidth, TotalHeight))
	white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 0xFF}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = white.R, white.G, white.B, white.A
	}
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			if s.pix[y*Width+x] {
				img.SetRGBA(BorderLeft+x, BorderTop+y, black)
			}
		}
	}
	return img
}

// halt is the Z80 HALT opcode ($76); in the display file it terminates a line.
const halt = 0x76

// RenderDisplayFile plots a ZX80/ZX81 display file: character bytes are drawn
// left-to-right and each HALT advances to the next text row. A single leading
// HALT (which precedes line 0 in the standard display file) is consumed.
// Characters beyond column 31 or row 23, and lines that end early, leave the
// remaining cells as paper.
func (s *Screen) RenderDisplayFile(dfile []byte) {
	i := 0
	if len(dfile) > 0 && dfile[0] == halt {
		i = 1 // leading HALT precedes line 0
	}
	row, col := 0, 0
	for ; i < len(dfile); i++ {
		b := dfile[i]
		if b == halt {
			row++
			col = 0
			continue
		}
		if row < Rows && col < Cols {
			s.PlotChar(col, row, b)
			col++
		}
	}
}

// CharGen decodes ZX80/ZX81 display bytes into 8-pixel scanline patterns using
// the character-generator data from the machine ROM. Each character occupies 8
// consecutive bytes (one per scanline), shifted out MSB-first.
type CharGen struct {
	charset []byte
}

// NewCharGen returns a CharGen backed by the given character-generator data
// (the character-set region of the ZX80/ZX81 ROM).
func NewCharGen(charset []byte) *CharGen {
	return &CharGen{charset: charset}
}

// Row returns the 8 horizontal pixels for scanline (0..7) of the character
// referenced by display byte b. Bit 7 of the result is the leftmost pixel; a
// set bit is ink (black).
func (g *CharGen) Row(b byte, scanline int) byte {
	code := b & 0x3F
	bits := g.charset[int(code)*8+scanline]
	if b&0x80 != 0 { // bit 7: inverse video
		bits ^= 0xFF
	}
	return bits
}
