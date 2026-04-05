package ula

import (
	"image"
	"image/color"
	"log"

	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Display constants
const (
	ScreenWidth   = 256 // Spectrum screen width in pixels
	ScreenHeight  = 192 // Spectrum screen height in pixels
	BorderLeft    = 32  // Left border width in pixels
	BorderTop     = 24  // Top border height in pixels
	TotalWidth    = ScreenWidth + BorderLeft*2  // 320
	TotalHeight   = ScreenHeight + BorderTop*2  // 240
	FlashFrames   = 16  // Number of frames between flash toggles
)

// ULA represents the Uncommitted Logic Array, handling video, sound, and keyboard.
type ULA struct {
	mem         *memory.Memory
	kbd         *keyboard.Keyboard
	audio       *audio.AudioSystem
	peripherals *peripherals.PeripheralManager
	img         *image.RGBA
	palette     [16]color.RGBA
	flash       bool
	flashCount  int

	// Port 0xFE state
	BorderColour byte
	Mic          bool
	TapeIn       bool
	Speaker      bool

	// Tape loading state
	tape       *TapePlayer
}

// New creates a new ULA instance.
func New(mem *memory.Memory, kbd *keyboard.Keyboard) *ULA {
	u := &ULA{
		mem: mem,
		kbd: kbd,
		img: image.NewRGBA(image.Rect(0, 0, TotalWidth, TotalHeight)),
	}
	u.initPalette()

	// Audio initialization is deferred to EnableAudio() to avoid crashes
	// in headless/test environments where audio hardware is unavailable.
	
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
	// Update tape player (one frame = 69888 T-states)
	if u.tape != nil && u.tape.IsPlaying() {
		u.TapeIn = u.tape.Update(69888)
	}

	u.flashCount++
	if u.flashCount >= FlashFrames {
		u.flash = !u.flash
		u.flashCount = 0
	}

	borderColor := u.palette[u.BorderColour]

	// Draw borders
	for y := 0; y < TotalHeight; y++ {
		for x := 0; x < TotalWidth; x++ {
			if x < BorderLeft || x >= BorderLeft+ScreenWidth || y < BorderTop || y >= BorderTop+ScreenHeight {
				u.img.Set(x, y, borderColor)
			}
		}
	}

	// Draw screen
	screenMem := u.mem.GetPage(u.mem.ScreenPage)
	attrMem := screenMem[0x1800:]

	for y := 0; y < ScreenHeight; y++ {
		for x := 0; x < ScreenWidth/8; x++ {
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
				px := BorderLeft + (x*8 + bit)
				py := BorderTop + y
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

	// Delegate to peripherals
	if u.peripherals != nil {
		if value, handled := u.peripherals.HandlePortRead(addr); handled {
			return value, true
		}
	}

	// Floating bus: return 0xFF for unhandled ports
	return 0xFF, false
}

// WritePort handles CPU writes to ULA-controlled ports.
func (u *ULA) WritePort(addr uint16, val byte) {
	if addr&0x01 == 0 { // Port 0xFE
		u.BorderColour = val & 0x07
		u.Mic = (val & 0x08) != 0
		
		// Handle speaker state change
		newSpeakerState := (val & 0x10) != 0
		if newSpeakerState != u.Speaker {
			u.Speaker = newSpeakerState
			// Update audio system if available (but don't log every change)
			if u.audio != nil {
				u.audio.SetSpeakerState(newSpeakerState)
			}
		}
	} else if addr&0x8002 == 0 { // Port 0x7FFD (128K memory paging): A15=0, A1=0
		// Only handle this on 128K+ models
		if u.mem.GetCurrentModel() != roms.Model48K {
			u.mem.PageMemory(val)
		}
	} else if addr&0xF002 == 0x1000 { // Port 0x1FFD (+3 special paging): A15=0, A14=0, A13=0, A12=1, A1=0
		if u.mem.GetCurrentModel() == roms.ModelPlus3 || u.mem.GetCurrentModel() == roms.ModelPlus2A {
			u.mem.PageMemoryPlus3(val)
		}
	}

	// Delegate to peripherals
	if u.peripherals != nil {
		u.peripherals.HandlePortWrite(addr, val)
	}
}

// Close properly shuts down the ULA and releases resources
func (u *ULA) Close() {
	if u.audio != nil {
		_ = u.audio.Close()
	}
}

// EnableAudio initializes and starts the audio system.
// Call this from the application (not tests) after creating the ULA.
func (u *ULA) EnableAudio() {
	audioSys, err := audio.New()
	if err != nil {
		log.Printf("Warning: Failed to initialize audio system: %v", err)
		return
	}
	u.audio = audioSys
	if err := u.audio.Start(); err != nil {
		log.Printf("Warning: Failed to start audio system: %v", err)
	}
}

// SetPeripherals sets the peripheral manager for I/O port delegation
func (u *ULA) SetPeripherals(pm *peripherals.PeripheralManager) {
	u.peripherals = pm
}

// SetTapePlayer sets the tape player for tape loading
func (u *ULA) SetTapePlayer(tp *TapePlayer) {
	u.tape = tp
}

// Reset resets the ULA to initial state
func (u *ULA) Reset() {
	u.BorderColour = 0
	u.Mic = false
	u.TapeIn = false
	u.Speaker = false
	u.flash = false
	u.flashCount = 0
	
	if u.audio != nil {
		u.audio.Reset()
	}
}