package ula

import (
	"image"
	"image/color"
	"log"
	"sync/atomic"

	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/ay"
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

// TStatesPerLine is the number of T-states per scanline (228 for 48K/128K).
const TStatesPerLine = 228

// ULA represents the Uncommitted Logic Array, handling video, sound, and keyboard.
type ULA struct {
	mem         *memory.Memory
	kbd         *keyboard.Keyboard
	audio       *audio.AudioSystem
	ay          *ay.AY
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

	// Kempston joystick state (port 0x1F).
	// Bit 0: Right, Bit 1: Left, Bit 2: Down, Bit 3: Up, Bit 4: Fire.
	// A bit is 1 when the corresponding direction/fire is active.
	KempstonEnabled bool
	KempstonState   byte

	// Mid-frame border tracking: records (scanline, colour) pairs for each border change.
	// Allows accurate rendering of border effects that change colour during the frame.
	borderChanges []borderChange

	// Beeper audio event recording. Each port-0xFE write that flips
	// bit 4 appends an (offset, state) tuple here. Render() walks the
	// list at end of frame to synthesise audio samples and pushes
	// them to the audio system. Reset at start of every frame.
	audioEvents            []audioEvent
	frameStartTstate       uint64
	frameStartSpeakerState bool

	// Tape loading state
	tape *TapePlayer

	// RZX playback/recording hooks. The RZX driver installs these
	// to intercept IN-port traffic: the playback hook substitutes
	// the recorded byte (skipping the real peripheral path), the
	// record hook logs the byte the peripherals returned. At most
	// one of the two should be set at a time — playback and
	// recording are mutually exclusive in FUSE (rzx.c:164, 278).
	//
	// Stored as atomic.Pointer because the UI thread installs and
	// clears them while the emulation goroutine reads them in
	// ReadPort — a plain func field would race.
	rzxPlaybackHook atomic.Pointer[func() (byte, bool)]
	rzxRecordHook   atomic.Pointer[func(byte)]
}

type borderChange struct {
	scanline int
	colour   byte
}

// audioEvent records a single speaker-bit toggle within a frame, with
// the T-state offset (0..tstatesPerFrame) at which it happened.
type audioEvent struct {
	tstateOffset int
	state        bool
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

	// AY-3-8912 sound chip is fitted on every model except the original 48K.
	if mem.GetCurrentModel() != roms.Model48K {
		u.ay = ay.New()
	}

	return u
}

// AY returns the AY-3-8912 sound chip instance, or nil for models that do
// not have one (e.g. the 48K).
func (u *ULA) AY() *ay.AY {
	return u.ay
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

	// Synthesise audio for the frame from recorded speaker events
	// and push to the audio system, then reset the per-frame state.
	u.flushAudioFrame()

	u.flashCount++
	if u.flashCount >= FlashFrames {
		u.flash = !u.flash
		u.flashCount = 0
	}

	// Build per-scanline border colour map from recorded changes.
	// Each display scanline (0-239) maps to a border colour.
	var borderPerLine [TotalHeight]byte
	if len(u.borderChanges) > 0 {
		// Start with the colour that was active before the first change in this frame.
		// If the first change isn't on scanline 0, the previous frame's final colour applies.
		currentBorder := u.BorderColour
		if u.borderChanges[0].scanline == 0 {
			currentBorder = u.borderChanges[0].colour
		}
		changeIdx := 0
		for line := 0; line < TotalHeight; line++ {
			// Advance past any border changes that apply to this scanline
			// Map display line to frame scanline (line 0 = top border start)
			frameScanline := line + (64 - BorderTop) // approximate: 64 lines before active display on 48K
			for changeIdx < len(u.borderChanges) && u.borderChanges[changeIdx].scanline <= frameScanline {
				currentBorder = u.borderChanges[changeIdx].colour
				changeIdx++
			}
			borderPerLine[line] = currentBorder
		}
	} else {
		for line := 0; line < TotalHeight; line++ {
			borderPerLine[line] = u.BorderColour
		}
	}
	u.borderChanges = u.borderChanges[:0] // Clear for next frame

	// Draw borders with per-scanline colours
	for y := 0; y < TotalHeight; y++ {
		borderColor := u.palette[borderPerLine[y]]
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

// ReadPort handles CPU reads from ULA-controlled ports. The single
// chokepoint at which the RZX driver intercepts the IN stream:
//
//  1. If RZX playback is active, the substitute byte is returned
//     directly without consulting any real peripheral.
//  2. Otherwise the normal port-dispatch logic runs.
//  3. If RZX recording is active, the resulting byte is logged so the
//     session can be replayed later.
//
// Mirrors FUSE's readport_internal at periph.c:310-355.
func (u *ULA) ReadPort(addr uint16) (byte, bool) {
	if hp := u.rzxPlaybackHook.Load(); hp != nil {
		if val, ok := (*hp)(); ok {
			return val, true
		}
		// Stream exhausted — fall through to normal dispatch.
	}

	val, handled := u.readPortInternal(addr)

	if hr := u.rzxRecordHook.Load(); hr != nil {
		(*hr)(val)
	}
	return val, handled
}

// readPortInternal contains the real port-dispatch logic, free of any
// RZX bookkeeping. Pulled out so ReadPort can sandwich it between the
// playback and recording hooks without duplicating dispatch code.
func (u *ULA) readPortInternal(addr uint16) (byte, bool) {
	if addr&0x01 == 0 { // Port 0xFE
		val := byte(0x1F) // Default value for unused bits
		if u.TapeIn {
			val |= 0x40
		}
		val &= u.kbd.Scan(addr)
		return val, true
	}

	// AY-3-8912 register read: port 0xFFFD on 128K+ models.
	// Decoded as A15=1, A14=1, A1=0 (addr & 0xC002 == 0xC000).
	if u.ay != nil && (addr&0xC002) == 0xC000 {
		return u.ay.ReadSelected(), true
	}

	// Kempston joystick: port 0x1F. Decoded as A7..A5 = 0 and A4..A0 = 0x1F.
	// We use the conventional decode (addr & 0x00E0 == 0 and addr & 0x001F == 0x001F).
	if u.KempstonEnabled && (addr&0x00E0) == 0x0000 && (addr&0x001F) == 0x001F {
		return u.KempstonState & 0x1F, true
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

// SetRZXPlaybackHook installs (or removes, with hook=nil) the RZX
// playback IN-byte source. The hook returns ok=true with the next
// recorded byte, or ok=false if the stream has been exhausted.
// Safe to call from any goroutine — the hook field is atomic.
func (u *ULA) SetRZXPlaybackHook(hook func() (byte, bool)) {
	if hook == nil {
		u.rzxPlaybackHook.Store(nil)
		return
	}
	u.rzxPlaybackHook.Store(&hook)
}

// SetRZXRecordHook installs (or removes, with hook=nil) the RZX
// recording sink. The hook is called once per IN-port read with the
// value the real peripherals returned, BEFORE that value is delivered
// to the CPU. Safe to call from any goroutine.
func (u *ULA) SetRZXRecordHook(hook func(byte)) {
	if hook == nil {
		u.rzxRecordHook.Store(nil)
		return
	}
	u.rzxRecordHook.Store(&hook)
}

// Kempston joystick bit constants for KempstonState.
const (
	KempstonRight = 0x01
	KempstonLeft  = 0x02
	KempstonDown  = 0x04
	KempstonUp    = 0x08
	KempstonFire  = 0x10
)

// SetKempstonButton sets or clears a Kempston joystick button bit.
func (u *ULA) SetKempstonButton(mask byte, pressed bool) {
	if pressed {
		u.KempstonState |= mask
	} else {
		u.KempstonState &^= mask
	}
}

// WritePort handles CPU writes to ULA-controlled ports.
func (u *ULA) WritePort(addr uint16, val byte) {
	if addr&0x01 == 0 { // Port 0xFE
		newBorder := val & 0x07
		if newBorder != u.BorderColour {
			// Record the border change with current scanline for mid-frame rendering
			scanline := 0
			if u.mem.TStates != nil {
				scanline = int(*u.mem.TStates / TStatesPerLine)
			}
			u.borderChanges = append(u.borderChanges, borderChange{scanline: scanline, colour: newBorder})
			u.BorderColour = newBorder
		}
		u.Mic = (val & 0x08) != 0
		
		// Handle speaker state change. Each toggle is recorded with
		// the T-state offset within the current frame so the audio
		// generator can reconstruct the waveform at end-of-frame.
		newSpeakerState := (val & 0x10) != 0
		if newSpeakerState != u.Speaker {
			u.Speaker = newSpeakerState
			if u.audio != nil && u.mem.TStates != nil {
				offset := int(*u.mem.TStates - u.frameStartTstate)
				u.audioEvents = append(u.audioEvents, audioEvent{
					tstateOffset: offset,
					state:        newSpeakerState,
				})
			}
		}
	} else if u.ay != nil && (addr&0xC002) == 0xC000 {
		// AY-3-8912 register select: port 0xFFFD on 128K+ models.
		// Decoded as A15=1, A14=1, A1=0.
		u.ay.SelectRegister(val)
	} else if u.ay != nil && (addr&0xC002) == 0x8000 {
		// AY-3-8912 data write: port 0xBFFD on 128K+ models.
		// Decoded as A15=1, A14=0, A1=0.
		u.ay.WriteSelected(val)
	} else if u.mem.GetCurrentModel() == roms.ModelPlus3 || u.mem.GetCurrentModel() == roms.ModelPlus2A {
		// +3/+2A use stricter port decoding to avoid conflicts between 0x7FFD and 0x1FFD:
		//   0x7FFD: mask=0xC002 value=0x4000 (A15=0, A14=1, A1=0)
		//   0x1FFD: mask=0xF002 value=0x1000 (A15=0, A14=0, A13=0, A12=1, A1=0)
		if addr&0xC002 == 0x4000 {
			u.mem.PageMemory(val)
		} else if addr&0xF002 == 0x1000 {
			u.mem.PageMemoryPlus3(val)
		}
	} else if addr&0x8002 == 0 { // Port 0x7FFD (128K memory paging): A15=0, A1=0
		// Only handle this on 128K+ models
		if u.mem.GetCurrentModel() != roms.Model48K {
			u.mem.PageMemory(val)
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

// StartRecording begins capturing the audio output to a WAV file. Returns
// nil if no audio system is available (in which case recording is silently
// skipped).
func (u *ULA) StartRecording(path string) error {
	if u.audio == nil {
		return nil
	}
	return u.audio.StartRecording(path)
}

// StopRecording finalises the active WAV recording, if any.
func (u *ULA) StopRecording() error {
	if u.audio == nil {
		return nil
	}
	return u.audio.StopRecording()
}

// IsRecording reports whether a WAV recording is currently in progress.
func (u *ULA) IsRecording() bool {
	if u.audio == nil {
		return false
	}
	return u.audio.IsRecording()
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
	if u.ay != nil {
		u.audio.SetAY(u.ay)
	}
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

// GetTapePlayer returns the currently loaded tape player (or nil).
func (u *ULA) GetTapePlayer() *TapePlayer {
	return u.tape
}

// Reset resets the ULA to initial state
func (u *ULA) Reset() {
	u.BorderColour = 0
	u.Mic = false
	u.TapeIn = false
	u.Speaker = false
	u.flash = false
	u.flashCount = 0
	u.KempstonState = 0

	if u.audio != nil {
		u.audio.Reset()
	}

	// Sync the AY presence with the current memory model. SwitchModel may
	// have changed the machine since the ULA was created, so we (re)create
	// the AY here for any 128K+ model and detach it on a plain 48K.
	if u.mem.GetCurrentModel() != roms.Model48K {
		if u.ay == nil {
			u.ay = ay.New()
			if u.audio != nil {
				u.audio.SetAY(u.ay)
			}
		} else {
			u.ay.Reset()
		}
	} else {
		if u.ay != nil {
			u.ay = nil
			if u.audio != nil {
				u.audio.SetAY(nil)
			}
		}
	}

	// Reset beeper sample generation state.
	u.audioEvents = u.audioEvents[:0]
	u.frameStartSpeakerState = false
	if u.mem.TStates != nil {
		u.frameStartTstate = *u.mem.TStates
	}
}

// flushAudioFrame synthesises the beeper waveform for the just-finished
// frame from the recorded speaker events, pushes it to the audio
// system, and resets the per-frame state for the next frame.
func (u *ULA) flushAudioFrame() {
	if u.audio == nil {
		return
	}
	samples, finalState := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)
	u.audio.PushBeeperSamples(samples)
	u.frameStartSpeakerState = finalState
	u.audioEvents = u.audioEvents[:0]
	if u.mem.TStates != nil {
		u.frameStartTstate = *u.mem.TStates
	}
}

// generateBeeperFrame synthesises one frame's worth of mono beeper
// samples from a list of speaker-toggle events. Returns the samples
// and the speaker state at the end of the frame so the caller can
// seed the next frame's initial state.
//
// Each output sample is the *average* speaker level over the T-state
// range that sample represents — i.e. a box-filter integration. This
// matters because the speaker can toggle far faster than the audio
// sample rate (BEEP runs at a few kHz, the audio rate is ~44kHz with
// ~79 T-states per sample), so a sample window can contain several
// transitions. Point-sampling at the midpoint loses the duty cycle
// inside the window and snaps every transition to a sample boundary,
// which produces audible time-jitter — the "fuzzy" sound the
// midpoint version had on a clean square wave. Integration converts
// the jitter into amplitude variation, which is much less perceptible
// and naturally low-pass-filters the output.
func generateBeeperFrame(events []audioEvent, initialState bool) (samples []int16, finalState bool) {
	const tstatesPerFrame = 69888
	samples = make([]int16, audio.SamplesPerFrame)
	state := initialState
	eventIdx := 0

	delta := int32(beeperHigh) - int32(beeperLow)
	low := int32(beeperLow)

	for i := 0; i < audio.SamplesPerFrame; i++ {
		sampleStart := i * tstatesPerFrame / audio.SamplesPerFrame
		sampleEnd := (i + 1) * tstatesPerFrame / audio.SamplesPerFrame
		sampleLen := sampleEnd - sampleStart

		// Walk events that fall inside [sampleStart, sampleEnd),
		// summing the T-states the speaker was high.
		highTstates := 0
		cur := sampleStart
		for eventIdx < len(events) && events[eventIdx].tstateOffset < sampleEnd {
			next := events[eventIdx].tstateOffset
			if next < cur {
				next = cur
			}
			if state {
				highTstates += next - cur
			}
			cur = next
			state = events[eventIdx].state
			eventIdx++
		}
		// Tail of the sample window (after the last event in it).
		if state {
			highTstates += sampleEnd - cur
		}

		if sampleLen > 0 {
			samples[i] = int16(low + delta*int32(highTstates)/int32(sampleLen))
		} else {
			samples[i] = beeperLow
		}
	}
	return samples, state
}

// Beeper amplitude levels — symmetric around zero so a constant
// 50% duty cycle averages to silence. The amplitude is high (~60%
// of int16 range) because on a real Spectrum the beeper is
// significantly louder than the AY chip — it's a 1-bit DAC driven
// directly by the speaker bit, with no attenuation. The remaining
// headroom (32767 − 20000 = 12767) is enough to mix in one AY
// channel at maximum volume without clipping; the worst case (all
// 3 AY channels at maximum + beeper at peak) is rare and clips
// gracefully via the int32 saturation in MixInto.
const (
	beeperHigh int16 = 20000
	beeperLow  int16 = -20000
)