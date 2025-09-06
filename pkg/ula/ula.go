package ula

import (
	"image"
	"image/color"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// ULA represents the Uncommitted Logic Array, handling video, sound, and keyboard.
type ULA struct {
	mem      *memory.Memory
	kbd      *keyboard.Keyboard
	img      *image.RGBA
	palette  [16]color.RGBA
	flash    bool
	flashCount int

	// Port 0xFE state
	BorderColour byte
	Mic          bool
	TapeIn       bool
	Speaker      bool
}

// New creates a new ULA instance.
func New(mem *memory.Memory, kbd *keyboard.Keyboard) *ULA {
	u := &ULA{
		mem: mem,
		kbd: kbd,
		img: image.NewRGBA(image.Rect(0, 0, 320, 240)), // 256x192 screen + 32/24 border
	}
	u.initPalette()
	return u
}

func (u *ULA) initPalette() {
	// Standard Spectrum palette (dark and bright versions)
	u.palette = [16]color.RGBA{
		// Dark
		{0, 0, 0, 255},       // Black
		{0, 0, 205, 255},     // Blue
		{205, 0, 0, 255},     // Red
		{205, 0, 205, 255},   // Magenta
		{0, 205, 0, 255},     // Green
		{0, 205, 205, 255},   // Cyan
		{205, 205, 0, 255},   // Yellow
		{205, 205, 205, 255}, // White
		// Bright
		{0, 0, 0, 255},       // Bright Black (same as dark)
		{0, 0, 255, 255},     // Bright Blue
		{255, 0, 0, 255},     // Bright Red
		{255, 0, 255, 255},   // Bright Magenta
		{0, 255, 0, 255},     // Bright Green
		{0, 255, 255, 255},   // Bright Cyan
		{255, 255, 0, 255},   // Bright Yellow
		{255, 255, 255, 255}, // Bright White
	}
}

// Render generates the current frame.
func (u *ULA) Render() *image.RGBA {
	u.flashCount++
	if u.flashCount >= 16 { // Flash roughly every 16 frames
		u.flash = !u.flash
		u.flashCount = 0
	}

	borderColor := u.palette[u.BorderColour]

	// Draw borders
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			if x < 32 || x >= 288 || y < 24 || y >= 216 {
				u.img.Set(x, y, borderColor)
			}
		}
	}

	// Draw screen
	screenMem := u.mem.GetPage(u.mem.ScreenPage)
	attrMem := screenMem[0x1800:]

	for y := 0; y < 192; y++ {
		for x := 0; x < 32; x++ {
			// Calculate address of pixel data and attribute data
			// This layout is non-linear
			addr := ((y & 0xC0) << 5) | ((y & 0x07) << 8) | ((y & 0x38) << 2) | x
			attrAddr := ((y >> 3) * 32) + x

			pixels := screenMem[addr]
			attr := attrMem[attrAddr]

			inkIdx := attr & 0x07
			paperIdx := (attr >> 3) & 0x07
			if (attr & 0x40) != 0 { // Bright
				inkIdx += 8
				paperIdx += 8
			}

			ink := u.palette[inkIdx]
			paper := u.palette[paperIdx]

			if u.flash && (attr&0x80) != 0 {
				ink, paper = paper, ink
			}

			for bit := 0; bit < 8; bit++ {
				px := 32 + (x*8 + bit)
				py := 24 + y
				if (pixels & (0x80 >> bit)) != 0 {
					u.img.Set(px, py, ink)
				} else {
					u.img.Set(px, py, paper)
				}
			}
		}
	}
	return u.img
}

// ReadPort handles CPU reads from ULA-controlled ports.
func (u *ULA) ReadPort(addr uint16) (byte, bool) {
	if addr&0x01 == 0 { // Port 0xFE
		val := byte(0x1F) // Default value for unused bits
		if u.TapeIn {
			val |= 0x40
		}
		val &= u.kbd.Scan(addr)
		return val, true
	}
	return 0, false
}

// WritePort handles CPU writes to ULA-controlled ports.
func (u *ULA) WritePort(addr uint16, val byte) {
	if addr&0x01 == 0 { // Port 0xFE
		u.BorderColour = val & 0x07
		u.Mic = (val & 0x08) != 0
		u.Speaker = (val & 0x10) != 0
	}
}