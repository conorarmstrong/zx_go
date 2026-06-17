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
	ScreenWidth  = 256                        // Spectrum screen width in pixels
	ScreenHeight = 192                        // Spectrum screen height in pixels
	BorderLeft   = 32                         // Left border width in pixels
	BorderTop    = 24                         // Top border height in pixels
	TotalWidth   = ScreenWidth + BorderLeft*2 // 320
	TotalHeight  = ScreenHeight + BorderTop*2 // 240
	FlashFrames  = 16                         // Number of frames between flash toggles
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
	// wideImg / wideRow are reused across frames for the 640-pixel
	// 80-column tilemap path (renderWide), so it doesn't allocate a
	// ~600 KB image every frame in the GUI's 50 Hz render loop.
	wideImg *image.RGBA
	wideRow []byte
	palette [16]color.RGBA
	// borderTracer, if non-nil, fires on every border-colour change
	// caused by an even-port write. Used by the debugger to observe
	// border modulation through any port that matches the ULA's
	// "even-address" decode (not just $FE), which a port-tracer
	// keyed by port number can miss.
	borderTracer func(port uint16, val byte, newBorder byte, scanline int)
	flash        bool
	flashCount   int

	// Port 0xFE state
	BorderColour byte
	Mic          bool
	TapeIn       bool
	// lastTapeTstate is the monotonic CPU T-state at which the tape was last
	// advanced. The tape is driven from each port-$FE read (tapeLevel), so the
	// EAR bit reflects the live tape level at microsecond resolution — which is
	// what edge-timed ROM and custom (turbo) loaders sample. (The old once-per-
	// frame Update froze the level for a whole 69888-T frame, so custom loaders
	// saw no pulses and never loaded.)
	lastTapeTstate uint64
	// Tape-loading sound: EAR-level transitions recorded during the frame so
	// flushAudioFrame can reconstruct the audible loading tone (the pilot
	// whistle + data screech) and mix it into the output — as a real 48K does
	// through the beeper and a 128K through the TV. Only recorded while the
	// tape is playing.
	tapeAudioEvents     []audioEvent
	frameStartTapeState bool
	Speaker             bool

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

	// dc models the capacitor-coupled audio output: it high-pass-filters the
	// per-frame mix so a held speaker level decays to silence instead of
	// sitting at a full-scale DC rail (which made power-on/reset/tape
	// boundaries click like a speaker wired to a battery). dcEnabled allows
	// disabling it (A/B diagnostics) — when off, the raw ±beeper levels are
	// emitted (faithful square waves, but the idle DC rail/click returns).
	dc        dcBlocker
	dcEnabled bool

	// fastLoad, when set, mutes audio output: during fast-tape turbo many
	// emulated frames collapse into one audio frame, so the reconstructed
	// loading sound is garbled. Silence is emitted instead.
	fastLoad bool

	// feReadCount is a monotonic count of port-$FE reads, used to detect
	// active tape loading by its read rate (see ReadPort).
	feReadCount uint64

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

	// nextRegs forwards port 0x243B / 0x253B traffic to the
	// Spectrum Next NextReg dispatcher when one has been wired
	// (ModelNext only). Stays nil on other models; the ports
	// then fall through to the existing floating-bus dispatch.
	nextRegs NextRegAccess

	// nextAY is the Spectrum Next's three-chip AY engine when
	// wired. When non-nil, port 0xFFFD / 0xBFFD traffic routes
	// to engine.Active() instead of the singleton u.ay. Stays
	// nil on every other model.
	nextAY *ay.Engine

	// nextCompositor blends Layer 2 (and, later, Tilemap and
	// Sprites) over the ULA's rendered framebuffer at the end
	// of each frame. Wired by the ModelNext bus during
	// construction; nil on every other model.
	nextCompositor NextCompositor

	// nextI2C receives port $103B/$113B SCL/SDA bit-bang traffic
	// (the DS1307 RTC bus). nil on classic models.
	nextI2C NextI2C
	// nextDMA receives port 0x6B writes (zxnDMA command stream).
	// Wired only for ModelNext.
	nextDMA NextDMA

	// nextSprite receives port $303B traffic: a write selects the
	// active sprite, a read returns the sprite status (collision /
	// max-per-line, clear-on-read). Wired only for ModelNext.
	nextSprite NextSpritePort

	// nextCopper is ticked once per active scanline during the
	// post-render compositor pass. nil on non-Next models.
	nextCopper NextCopper

	// nextDAC receives the four DAC channel port writes. Decoded
	// on low byte only — channels A/B/C/D map to several alias
	// ports per the SpecNext wiki. Wired only for ModelNext.
	nextDAC NextDAC

	// nextDivMMC receives port 0xE3 writes (divMMC control:
	// CONMEM / MAPRAM / bank-select). Wired only for ModelNext.
	nextDivMMC NextDivMMC

	// speccyDAC is the classic-Spectrum 8-bit DAC pair (SpecDrum on $DF,
	// Covox on $FB). Wired on classic models when the user enables either
	// peripheral; nil otherwise. Its writes are recorded with T-state offsets
	// and mixed into the beeper output at end-of-frame.
	speccyDAC SpeccyDAC

	// beta is the Beta Disk / TR-DOS interface, wired on classic models when a
	// disk is mounted; nil otherwise. Its ports are decoded only while the
	// TR-DOS ROM is paged in (mem.IsBetaActive).
	beta BetaDisk

	// port123BVal shadows the last byte written to the Layer 2 port
	// $123B (FPGA signal port_123b_dat, reset 0). IN $123B returns it
	// (zxnext.vhd:2822); the 128K launch's MF NMI handler reads it to
	// snapshot Layer 2 state.
	port123BVal byte

	// portTracer, when non-nil, fires after every port read or
	// write that completes through WritePort / ReadPort. Set via
	// SetPortTracer; nil at the zero value so the trace path is
	// one nil-check per access when disabled.
	portTracer PortTracer
}

// PortTracer is the callback signature for ULA port I/O tracing.
// The handled flag indicates whether the ULA produced a value
// (true) or fell through to floating-bus / open-bus (false).
type PortTracer func(addr uint16, val byte, write, handled bool)

// NextCompositor is the contract the ULA uses to ask the
// Spectrum Next render stack for a composited scanline. The only
// implementation today is pkg/next/compositor.Compositor; the
// interface lives in pkg/ula so the package doesn't have to
// import pkg/next/compositor (which would invite a cycle once
// the compositor pulls in more pkg/ula state for the sprite
// bandwidth model in Sprint 7).
type NextCompositor interface {
	ComposeScanline(y int, ulaRGBA []byte, dst []byte)
	// HasActiveTilemap reports whether the compositor has a
	// tilemap layer wired AND enabled. ULA uses this to decide
	// whether to run the border-area pass for Layer-3 content
	// that extends beyond the classic 256-wide inner screen.
	HasActiveTilemap() bool
	// ComposeBorderRow paints tilemap content over the border
	// pixels of a 320-wide RGBA row. tilemapY is the row index
	// within the tilemap (0 = top of the full 320×256 Next
	// display). isInBorderArea(x) returns true for x values
	// outside the classic 256-wide inner screen; those are the
	// pixels the border pass paints, leaving inner pixels
	// untouched.
	ComposeBorderRow(tilemapY int, dst []byte, isInBorderArea func(x int) bool)
	// TilemapIs80Col reports whether the tilemap is in 80-column
	// (640-pixel) mode. When true the ULA renders the wide path
	// (renderWide) and the 320-pixel passes above skip the tilemap.
	TilemapIs80Col() bool
	// ComposeWideTilemapRow composites the native 640-pixel tilemap
	// over dst, a 640-pixel RGBA row already holding the doubled lower
	// layers.
	ComposeWideTilemapRow(tilemapY int, dst []byte)
	// HiResLayer2Active reports whether Layer 2 is in a hi-res mode
	// (NR$70 resolution 1/2). When true the ULA renders the wide Layer 2
	// path (renderHiResLayer2) and the 256-wide pass skips Layer 2.
	HiResLayer2Active() bool
	// Layer2Width returns the active Layer 2 width (256/320/640).
	Layer2Width() int
	// ComposeWideLayer2Row overlays the hi-res Layer 2 row onto dst, an
	// RGBA row Layer2Width pixels wide already holding the lower layers.
	ComposeWideLayer2Row(y int, dst []byte)
}

// NextSpritePort is the contract for port $303B: SelectSprite on a
// write (sets the active sprite index), ReadStatus on a read (sprite
// status — bit 0 collision, bit 1 max-per-line — clear-on-read).
// pkg/next/sprite.Engine satisfies it.
type NextSpritePort interface {
	SelectSprite(v byte)
	ReadStatus() byte
}

// NextDMA is the contract for port 0x6B (zxnDMA command stream).
// pkg/next/dma.DMA satisfies it: WriteCommand consumes the WR-register byte
// stream; ReadCommand returns the next register in the read-mask sequence (an
// IO read of port 0x6B).
type NextDMA interface {
	WriteCommand(val byte)
	ReadCommand() byte
}

// NextI2C is the contract for the Spectrum Next's bit-banged i2c bus
// on ports $103B (SCL) and $113B (SDA) — zxnext.vhd:2630-2631 decode
// + :3234-3250 write latches. The DS1307 RTC slave lives behind it
// (pkg/next/rtc.Bus).
type NextI2C interface {
	WriteSCL(bit bool)
	WriteSDA(bit bool)
	ReadSDA() bool
}

// NextCopper is the contract the per-frame render loop uses to
// drive the Spectrum Next Copper coprocessor. pkg/next/copper.Copper
// satisfies it. The compositor calls Step once per active scanline
// so MOVEs that affect palette / Layer 2 state take effect before
// the row composites.
type NextCopper interface {
	Step(scanline uint16, hpos byte, maxInstr int) int
}

// NextDAC is the contract for the four Spectrum Next DAC channels.
// pkg/next/dac.Bank satisfies it via WritePort (which returns
// "handled?" so the ULA's port dispatcher knows whether to fall
// through). The ULA forwards every port write to the DAC; the bank
// internally checks the low byte for one of the documented DAC
// ports and ignores everything else.
type NextDAC interface {
	WritePort(port uint16, val byte) bool
}

// SpeccyDAC is the contract for the classic-Spectrum SpecDrum/Covox 8-bit DAC.
// pkg/audiodac.DAC satisfies it. The ULA claims the device's ports, records
// each write with its T-state offset, and mixes a reconstructed frame into the
// beeper output.
type SpeccyDAC interface {
	Handles(low byte) bool
	Record(tstateOffset int, val byte)
	Enabled() bool
	GenerateFrame(samplesPerFrame, tstatesPerFrame int) []int16
}

// BetaDisk is the contract for the Beta Disk / TR-DOS interface.
// pkg/betadisk.Interface satisfies it. The ULA only routes I/O to it while the
// TR-DOS ROM is paged in (Memory.IsBetaActive) — so the Beta's $1F/$FF decode
// doesn't shadow the Kempston joystick / floating bus during ordinary games.
type BetaDisk interface {
	Handles(port uint16) bool
	ReadPort(port uint16) byte
	WritePort(port uint16, val byte)
}

// NextDivMMC is the contract for the divMMC control port (0xE3 on
// the low byte). pkg/next/divmmc.Pager satisfies it. NextZXOS's
// boot trampoline writes to 0xE3 to drop the divMMC overlay; its
// IRQ handler reads 0xE3 to capture the current state before
// modifying it. Without both directions wired the boot deadlocks.
type NextDivMMC interface {
	WritePort(port uint16, val byte) bool
	ReadPort(port uint16) (byte, bool)
}

// NextRegAccess is the contract the ULA uses to forward port 0x243B
// (select latch) and 0x253B (data port) traffic into the Spectrum
// Next register file.
//
// The interface is declared here rather than in pkg/next/nextregs
// because Go's preferred style is to define interfaces at the
// consumer site. The concrete type implementing it lives in
// pkg/next/nextregs; pkg/ula must NOT import that package, which
// would invite a cycle once the nextregs callbacks need to invoke
// other ULA-side state.
//
// On non-Next models nothing wires a NextRegAccess in, so the port
// dispatch falls through to the existing 0xFE / 0xFFFD / floating-
// bus paths exactly as before.
type NextRegAccess interface {
	Select(reg byte)
	Selected() byte
	WriteData(val byte)
	ReadData() byte
	// WriteReg writes directly to a register without disturbing
	// the current Selected() latch. Used by classic-port aliases
	// (port $123B → NR$69, etc.) where the legacy I/O point has
	// to drive the same backing state as the NextReg form.
	WriteReg(reg, val byte)
	ReadReg(reg byte) byte
}

// SetNextRegs installs the NextReg port handler. Called once during
// ModelNext construction; passing nil unhooks (useful for tests).
func (u *ULA) SetNextRegs(n NextRegAccess) { u.nextRegs = n }

// SetNextCompositor installs the Spectrum Next render stack's
// scanline compositor. Once installed, Render overlays the
// composited output on top of the 256x192 active display region.
// Passing nil restores the plain-ULA render.
func (u *ULA) SetNextCompositor(c NextCompositor) { u.nextCompositor = c }

// Palette returns the ULA's 16-colour palette. The Next compositor uses it
// to resolve the ULA transparency colour: the classic ULA renders via this
// palette, so the global transparency NR$14 (when < 16) corresponds to
// u.palette[NR$14], which is the colour a transparent ULA pixel carries.
func (u *ULA) Palette() [16]color.RGBA { return u.palette }

// SetNextDMA installs the Spectrum Next zxnDMA controller. Port
// 0x6B writes are forwarded as command bytes. Passing nil unhooks.
func (u *ULA) SetNextDMA(d NextDMA) { u.nextDMA = d }

// SetNextSpritePort installs the sprite engine's $303B select/status
// port handler. Passing nil unhooks.
func (u *ULA) SetNextSpritePort(s NextSpritePort) { u.nextSprite = s }

// SetNextI2C installs the Spectrum Next i2c bus (RTC at $68). Ports
// $103B / $113B dispatch to it when present.
func (u *ULA) SetNextI2C(b NextI2C) { u.nextI2C = b }

// SetNextCopper installs the Spectrum Next Copper coprocessor.
// The compositor pass calls Step once per active scanline so MOVEs
// affecting palette / Layer 2 state are visible to that row's
// composition. Passing nil unhooks.
func (u *ULA) SetNextCopper(c NextCopper) { u.nextCopper = c }

// SetNextDAC installs the Spectrum Next four-channel DAC bank.
// Port writes are forwarded to it after the NextRegs / DMA priority
// checks; the bank internally decodes whether the low byte is one
// of its channels. Passing nil unhooks both the port path and any
// previously-attached mixer source so switching back to a classic
// model silences the DAC cleanly.
//
// If the audio mixer has already been started (via EnableAudio),
// the bank is also wired into it so a runtime model switch picks
// up the DAC immediately without having to restart audio.
func (u *ULA) SetNextDAC(d NextDAC) {
	u.nextDAC = d
	// The Next DAC is mixed event-timed in flushAudioFrame (see its
	// GenerateFrame), not via the audio system's per-pull DACSource path.
}

// SetSpeccyDAC attaches the classic-Spectrum SpecDrum/Covox DAC. Unlike the
// Next DAC it is event-timed: the ULA records its writes with T-state offsets
// and mixes a reconstructed frame into the beeper at end-of-frame (see
// flushAudioFrame), so PCM playback is sample-accurate. Pass nil to detach.
func (u *ULA) SetSpeccyDAC(d SpeccyDAC) { u.speccyDAC = d }

// SetBetaDisk attaches (or, with nil, detaches) the Beta Disk / TR-DOS
// interface. Port I/O is gated on Memory.IsBetaActive so it only intercepts the
// $1F/$3F/$5F/$7F/$FF ports while the TR-DOS ROM is paged in.
func (u *ULA) SetBetaDisk(d BetaDisk) { u.beta = d }

// betaClaims reports whether the Beta interface should handle this port now:
// it must be wired, the TR-DOS ROM paged in, and the port one of its registers.
func (u *ULA) betaClaims(addr uint16) bool {
	return u.beta != nil && u.mem != nil && u.mem.IsBetaActive() && u.beta.Handles(addr)
}

// SetNextDivMMC installs the divMMC pager's port-write hook so
// OUT (0xE3) reaches it. The pager itself is also wired via the
// CPU M1 pre-fetch hook (for automap on trigger PCs) and via
// memory.PeripheralRead/Write (for the 0x0000-0x3FFF overlay).
func (u *ULA) SetNextDivMMC(d NextDivMMC) { u.nextDivMMC = d }

// NextDivMMC returns the currently-wired divMMC pager (nil if
// none). Exposed so tests and debug tools can poke at pager
// state without going through the port interface.
func (u *ULA) NextDivMMC() NextDivMMC { return u.nextDivMMC }

// SetPortTracer installs a per-access callback fired after every
// port read and write that completes through ReadPort / WritePort.
// Pass nil to disable. Used by the `--trace=ports` CLI path.
func (u *ULA) SetPortTracer(fn PortTracer) { u.portTracer = fn }

// GetPortTracer returns the currently-installed PortTracer (or
// nil). Used by chained-tracer patterns where a new caller wants
// to run alongside any pre-existing tracer without losing it.
func (u *ULA) GetPortTracer() PortTracer { return u.portTracer }

// SetNextAY installs the Spectrum Next's three-chip AY engine.
// When set, port 0xFFFD / 0xBFFD traffic dispatches to the
// currently-active chip per NextReg 0x06's chip-select. Passing
// nil restores the single-AY routing.
func (u *ULA) SetNextAY(e *ay.Engine) {
	u.nextAY = e
	// Route the engine into the audio mixer so its (TurboSound) chips are
	// actually heard. Without this the mixer kept pulling from the single
	// u.ay — a chip the Next's port writes never reach — so 128K/AY music was
	// silent on the Next. SetNextAY runs after EnableAudio during Next setup,
	// so this is where the swap has to happen.
	if u.audio != nil {
		if e != nil {
			u.audio.SetAY(e)
		} else if u.ay != nil {
			u.audio.SetAY(u.ay)
		}
	}
}

// activeAY returns the AY chip that should currently service port
// 0xFFFD / 0xBFFD traffic. On ModelNext with an Engine wired, this
// is engine.Active() — unless the engine is in disabled mode, in
// which case nil is returned and AY port writes are silently
// dropped (matching real hardware's "AY disabled" bit). On every
// other configuration it returns the singleton u.ay.
func (u *ULA) activeAY() *ay.AY {
	if u.nextAY != nil {
		if u.nextAY.Disabled() {
			return nil
		}
		return u.nextAY.Active()
	}
	return u.ay
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
	// Bound the DC-blocked audio to the speaker's physical amplitude so an
	// isolated speaker toggle clicks at the level, not the high-pass's 2x
	// step-response overshoot.
	u.dc.limit = int32(beeperHigh)
	u.dcEnabled = true
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
// SetBorderTracer installs a callback fired on every ULA border-
// colour change (whatever even-address port was used).
func (u *ULA) SetBorderTracer(fn func(port uint16, val byte, newBorder byte, scanline int)) {
	u.borderTracer = fn
}

func (u *ULA) Render() *image.RGBA {
	// The tape EAR level is advanced per port-$FE read (tapeLevel), not here —
	// a once-per-frame Update would freeze the level for the whole frame and
	// starve edge-timed loaders.

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

	// Spectrum Next overlay: if a compositor is wired (ModelNext),
	// blend Layer 2 (and, later, Tilemap and Sprites) over the
	// active display region row by row. The compositor pulls
	// Layer 2 data internally; we just hand it the existing ULA
	// scanline and write the result back.
	if u.nextCompositor != nil {
		u.applyNextCompositor()
		if u.nextCompositor.HiResLayer2Active() {
			// Layer 2 in 320×256 / 640×256 hi-res mode spans the full
			// display width; composite it over the base frame.
			return u.renderHiResLayer2()
		}
		if u.nextCompositor.TilemapIs80Col() {
			// 80-column tilemap = 640px wide; render the wide frame.
			return u.renderWide()
		}
	}

	return u.img
}

// renderWide builds a 640×TotalHeight frame for 80-column tilemap mode.
// The 320-pixel base (ULA + Layer 2 + sprites — the tilemap was skipped
// in the 320-pixel passes) is horizontally pixel-doubled, then the
// native 640-pixel tilemap is composited on top. This is the faithful
// representation of the Next's 80-column tilemap, which runs the tilemap
// layer at double the horizontal pixel clock (640px) over the 320px ULA.
func (u *ULA) renderWide() *image.RGBA {
	const ww = 2 * TotalWidth // 640
	if u.wideImg == nil {
		u.wideImg = image.NewRGBA(image.Rect(0, 0, ww, TotalHeight))
		u.wideRow = make([]byte, ww*4)
	}
	wide := u.wideImg
	rowWide := u.wideRow
	for y := 0; y < TotalHeight; y++ {
		srcStart := y * u.img.Stride
		for x := 0; x < TotalWidth; x++ {
			s := srcStart + x*4
			r, g, b, a := u.img.Pix[s+0], u.img.Pix[s+1], u.img.Pix[s+2], u.img.Pix[s+3]
			d := x * 8
			rowWide[d+0], rowWide[d+1], rowWide[d+2], rowWide[d+3] = r, g, b, a
			rowWide[d+4], rowWide[d+5], rowWide[d+6], rowWide[d+7] = r, g, b, a
		}
		u.nextCompositor.ComposeWideTilemapRow(y, rowWide)
		dstStart := y * wide.Stride
		copy(wide.Pix[dstStart:dstStart+ww*4], rowWide)
	}
	return wide
}

// renderHiResLayer2 builds the frame for a hi-res Layer 2 mode (NR$70
// resolution 1 = 320×256, 2 = 640×256). The base frame (ULA + border +
// sprites + tilemap — Layer 2 was skipped in the 256-wide pass) is the
// lower layer; the native-width Layer 2 is composited on top (SLU-default
// priority — Layer 2 above ULA). For 640 the 320 base is pixel-doubled.
// The frame height stays TotalHeight (240): the hi-res L2's rows 0..239 are
// shown; the bottom 16 of its 256 lines fall outside our visible window
// (a documented simplification of the full 256-line hi-res display).
func (u *ULA) renderHiResLayer2() *image.RGBA {
	w := u.nextCompositor.Layer2Width()
	if w <= TotalWidth {
		// 320-wide: composite directly into the existing 320-wide img.
		row := make([]byte, w*4)
		for y := 0; y < TotalHeight; y++ {
			start := y * u.img.Stride
			copy(row, u.img.Pix[start:start+w*4])
			u.nextCompositor.ComposeWideLayer2Row(y, row)
			copy(u.img.Pix[start:start+w*4], row)
		}
		return u.img
	}
	// 640-wide: pixel-double the 320 base, then overlay the 640 L2.
	const ww = 2 * TotalWidth
	if u.wideImg == nil {
		u.wideImg = image.NewRGBA(image.Rect(0, 0, ww, TotalHeight))
		u.wideRow = make([]byte, ww*4)
	}
	wide := u.wideImg
	rowWide := u.wideRow
	for y := 0; y < TotalHeight; y++ {
		srcStart := y * u.img.Stride
		for x := 0; x < TotalWidth; x++ {
			s := srcStart + x*4
			r, g, b, a := u.img.Pix[s+0], u.img.Pix[s+1], u.img.Pix[s+2], u.img.Pix[s+3]
			d := x * 8
			rowWide[d+0], rowWide[d+1], rowWide[d+2], rowWide[d+3] = r, g, b, a
			rowWide[d+4], rowWide[d+5], rowWide[d+6], rowWide[d+7] = r, g, b, a
		}
		u.nextCompositor.ComposeWideLayer2Row(y, rowWide)
		dstStart := y * wide.Stride
		copy(wide.Pix[dstStart:dstStart+ww*4], rowWide)
	}
	return wide
}

// ActiveVideoLine returns the current raster line within the frame,
// derived from the CPU's T-state position (T-states since frame start /
// T-states-per-line). It is a 9-bit counter (0..511). The Spectrum Next
// exposes this via NextReg $1E (MSB, bit 0) / $1F (LSB): NextZXOS dot
// commands such as NextGuide DISABLE interrupts and poll it to sync to
// the raster, so it MUST advance as the CPU runs or the wait hangs.
func (u *ULA) ActiveVideoLine() int {
	line, _ := u.BeamPosition()
	return line
}

// BeamPosition returns the current raster beam position derived from the
// CPU T-state counter: the scanline (0-based, 9-bit) and the horizontal
// position in 8-pixel units (2 pixels per T-state, 8 pixels per hpos unit,
// so hpos = (T-state-in-line)/4 → 0..56 across a 228-T-state line). This
// lets the Copper, memory contention and (eventually) a per-scanline ULA
// renderer query the beam mid-frame at per-T-state granularity instead of
// the coarse scanline quantum. Returns (0,0) when no T-state source is
// wired.
func (u *ULA) BeamPosition() (line, hpos int) {
	if u.mem == nil || u.mem.TStates == nil {
		return 0, 0
	}
	t := int(*u.mem.TStates - u.frameStartTstate)
	if t < 0 {
		t = 0
	}
	line = (t / TStatesPerLine) & 0x1FF
	hpos = (t % TStatesPerLine) / 4
	return line, hpos
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
	if u.portTracer != nil {
		u.portTracer(addr, val, false /*write*/, handled)
	}
	return val, handled
}

// readPortInternal contains the real port-dispatch logic, free of any
// RZX bookkeeping. Pulled out so ReadPort can sandwich it between the
// playback and recording hooks without duplicating dispatch code.
func (u *ULA) readPortInternal(addr uint16) (byte, bool) {
	// Spectrum Next NextReg ports. Data port (0x253B) reads return
	// whatever the dispatcher's currently-selected register says.
	// Select port (0x243B) reads back the selected register NUMBER
	// (zxnext.vhd:4603 `port_243b_dat <= nr_register`) — NextZXOS's
	// IM1 handler saves the guest's selection with an IN here on
	// entry and restores it at the handler tail ($2040 OUT (C),L);
	// a write-only select port floats $FF into that save and every
	// interrupt then corrupts the guest's NR-select (the development log).
	if u.nextRegs != nil {
		switch addr {
		case 0x253B:
			return u.nextRegs.ReadData(), true
		case 0x243B:
			return u.nextRegs.Selected(), true
		}
	}

	// Beta Disk / TR-DOS registers, while the TR-DOS ROM is paged in. Checked
	// ahead of the Kempston joystick ($1F) and floating bus ($FF) so the FDC
	// wins those ports during a disk operation.
	if u.betaClaims(addr) {
		return u.beta.ReadPort(addr), true
	}

	// Port 0x6B: zxnDMA register read-back (status / byte counter / port
	// addresses, selected by the read mask). Decoded on the low 8 bits.
	if u.nextDMA != nil && (addr&0xFF) == 0x6B {
		return u.nextDMA.ReadCommand(), true
	}

	// Multiface 3 paging-register readback. Per the FPGA source
	// (zxnext.vhd:2612-2616 port_mf_enable decode + the mf_port_dat mux,
	// and multiface.vhd:43-44): while the Multiface is active (paged in /
	// "invisible off") in MF+3 mode, an IN whose LOW byte is $3F returns a
	// paging register selected by A15:12 —
	//   $7F3F -> port $7FFD (full byte)   (mf_port_dat: A15:12 = 0111)
	//   $1F3F -> port $1FFD (low nibble)  (mf_port_dat: A15:12 = 0001 =
	//            "0000" & !motor & 1ffd_reg(2:0))
	// NextZXOS's 128K-BASIC launch fires the MF NMI; its handler reads
	// $7F3F/$1F3F to snapshot the live paging into MF RAM ($3FCC/$3FFF),
	// then a routine ($15F9) tests those bytes. Returning open bus ($FF)
	// here set $3FCC bit4, flipping a `cp $04; jr nz` at MF ROM $01F6 and
	// routing the launch to the abort path instead of the Sinclair 128
	// menu (found via the ours-vs-reference launch instruction-trace diff).
	// The $Dxxx/$Exxx (dffd/eff7) and border high-nibble cases aren't
	// modelled — ours doesn't track those registers and the launch
	// doesn't read them.
	if u.mem != nil && u.mem.MultifaceActive() && addr&0x00FF == 0x003F {
		p7ffd, p1ffd, _ := u.mem.GetPortState()
		switch addr >> 12 {
		case 0x7:
			return p7ffd, true
		case 0x1:
			return p1ffd & 0x0F, true
		}
	}

	// Port $123B (Layer 2) readback: returns the last value written
	// (zxnext.vhd:2822 port_123b_rd_dat <= port_123b_dat). The 128K
	// launch's MF NMI handler reads $123B to snapshot Layer 2; open bus
	// here saved bit1=1, leaving NR$69 = $C0 (Layer 2 visible) at the
	// 128 menu and bleeding striping into the top border.
	if u.nextRegs != nil && addr == 0x123B {
		return u.port123BVal, true
	}

	// Port $303B read: sprite status (bit 0 collision, bit 1
	// max-per-line); reading clears the latched collision flag.
	if u.nextSprite != nil && addr == 0x303B {
		return u.nextSprite.ReadStatus(), true
	}

	// Port $113B: i2c SDA line read-back (bit 0; upper bits float
	// high — open-drain bus). Port $103B reads return the SCL latch
	// the same way on real hardware but NextZXOS never reads it; we
	// serve SDA only and leave $103B to the float path.
	if u.nextI2C != nil && addr == 0x113B {
		v := byte(0xFE)
		if u.nextI2C.ReadSDA() {
			v |= 0x01
		}
		return v, true
	}

	// divMMC control register read-back (port 0xE3). The divMMC
	// IRQ handler does IN A,(0xE3) to capture the current state.
	if u.nextDivMMC != nil {
		if val, ok := u.nextDivMMC.ReadPort(addr); ok {
			return val, true
		}
	}

	if addr&0x01 == 0 { // Port 0xFE
		// Per ZX Spectrum ULA spec: bits 0-4 are the keyboard
		// matrix half-row, bit 5 is reserved (reads 1), bit 6 is
		// the tape EAR signal (0 normally, 1 when TapeIn drives it),
		// bit 7 is reserved (reads 1). The original code used
		// 0x1F as the base — that forced bits 5 and 7 to zero
		// alongside bit 6, which is wrong for the reserved bits.
		// Spectrum Next's boot.bin (and Sinclair Test ROMs)
		// distinguish "live ULA" from "stuck bus" by reading
		// those reserved bits as 1; a zero there sends them
		// into error-handling paths. Fix: base value is 0xBF
		// (bit 6 = 0 default, bits 5 and 7 = 1) and the AND with
		// the keyboard scan ORs in 0xE0 so the kbd matrix only
		// affects bits 0-4.
		// Count port-$FE reads. A tape loader polls this register thousands
		// of times per frame to time edges, whereas a running game reads it
		// only sparsely for the keyboard — so the rate cleanly distinguishes
		// "actively loading" from "game running", which the fast-load turbo
		// uses to know when to stop accelerating.
		u.feReadCount++
		val := byte(0xBF)
		if u.tapeLevel() {
			val |= 0x40
		}
		val &= u.kbd.Scan(addr) | 0xE0
		return val, true
	}

	// AY-3-8912 register read: port 0xFFFD on 128K+ models.
	// Decoded as A15=1, A14=1, A1=0 (addr & 0xC002 == 0xC000).
	// On ModelNext this routes through the engine's currently-
	// active chip (NextReg 0x06 chip-select).
	if chip := u.activeAY(); chip != nil && (addr&0xC002) == 0xC000 {
		return chip.ReadSelected(), true
	}

	// Delegate to peripherals before Kempston — plug-in hardware
	// (DISCiPLE, IF1, etc.) intercepts the bus first. The DISCiPLE
	// control register at port 0x1F conflicts with Kempston; when the
	// DISCiPLE is active it takes priority, matching real hardware.
	if u.peripherals != nil {
		if value, handled := u.peripherals.HandlePortRead(addr); handled {
			return value, true
		}
	}

	// Kempston joystick: port 0x1F. Decoded as A7..A5 = 0 and A4..A0 = 0x1F.
	if u.KempstonEnabled && (addr&0x00E0) == 0x0000 && (addr&0x001F) == 0x001F {
		return u.KempstonState & 0x1F, true
	}

	// Floating-bus: on 48K and 128K, an unattached IN returns
	// whichever byte the ULA is currently fetching from screen
	// memory (or 0xFF during border/retrace/idle bus phases).
	// The +2A/+3 memory controller disables this behaviour;
	// ModelNext also returns 0xFF for compatibility with most
	// post-Sinclair software that's clean about port use.
	return u.floatingBusByte(), false
}

// floatingBusByte computes the value an unattached IN returns
// based on the current scanline / column timing. Implements the
// canonical algorithm documented by Ramsoft (1999) and FUSE
// (spectrum.c:spectrum_unattached_port). Used by some games
// (Arkanoid, Sidewize, Cobra, Short Circuit) for fast
// attribute readback. Returns 0xFF on +2A/+3 (no floating bus)
// and on ModelNext.
func (u *ULA) floatingBusByte() byte {
	if u.mem == nil || u.mem.TStates == nil {
		return 0xFF
	}
	model := u.mem.GetCurrentModel()
	if model == roms.ModelPlus2A || model == roms.ModelPlus3 || model == roms.ModelNext {
		return 0xFF
	}

	// Compute T-state offset within the current frame.
	tstates := int(*u.mem.TStates - u.frameStartTstate)

	// Top border: before the first display line.
	const topBorderTStates = 64 * TStatesPerLine
	if tstates < topBorderTStates {
		return 0xFF
	}

	line := (tstates - topBorderTStates) / TStatesPerLine
	if line >= 192 { // bottom border
		return 0xFF
	}

	// T-states into this line. The first 18 are the leftmost
	// blanking/sync; FUSE counts from the first displayed pixel
	// (T-state 14336 on 48K). Our frameStartTstate is the start
	// of frame, so we subtract the per-line origin.
	tInLine := tstates - topBorderTStates - line*TStatesPerLine

	// Each line: 24 T-states left border, 128 T-states display,
	// 24 right border, 52 retrace. Only the 128 display T-states
	// produce floating-bus data.
	const leftBorder = 24
	const horizontalScreen = 128
	if tInLine < leftBorder {
		return 0xFF
	}
	if tInLine >= leftBorder+horizontalScreen {
		return 0xFF
	}

	tInDisplay := tInLine - leftBorder
	// 8 T-states per 16-pixel column pair. Within those 8 T-states
	// the ULA's fetch pattern is:
	//   t%8 = 0,1: idle bus (0xFF)
	//   t%8 = 2:   bitmap[col]
	//   t%8 = 3:   attribute[col]
	//   t%8 = 4:   bitmap[col+1]
	//   t%8 = 5:   attribute[col+1]
	//   t%8 = 6,7: idle bus (0xFF)
	column := (tInDisplay / 8) * 2

	// Screen memory: bank 5 always holds the displayed screen on
	// 48K; on 128K the bank selected by 7FFD bit 3 holds it
	// (bank 5 or 7). The Memory accessor returns the active
	// screen page.
	screenBank := u.mem.ScreenPage
	if screenBank == 0 {
		screenBank = 5
	}
	page := u.mem.GetPage(screenBank)
	if page == nil {
		return 0xFF
	}

	switch tInDisplay % 8 {
	case 2:
		return page[screenAddrForRowCol(line, column)]
	case 3:
		return page[0x1800+(line/8)*32+column]
	case 4:
		return page[screenAddrForRowCol(line, column+1)]
	case 5:
		return page[0x1800+(line/8)*32+column+1]
	}
	return 0xFF
}

// screenAddrForRowCol returns the offset within a 16K screen RAM
// page where pixel-row `row` (0..191), column `col` (0..31, units
// of 8 pixels) is stored. The Spectrum's interleaved screen
// layout: row bits are scrambled as `010 765 432 1xx` to give the
// distinctive thirds-rotated memory map.
func screenAddrForRowCol(row, col int) int {
	if col < 0 || col > 31 || row < 0 || row > 191 {
		return 0
	}
	// y = bits y7..y0; address = (y7y6 << 11) | (y2y1y0 << 8) | (y5y4y3 << 5) | col
	y := uint(row)
	addr := ((y & 0xC0) << 5) | ((y & 0x07) << 8) | ((y & 0x38) << 2) | uint(col)
	return int(addr)
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

// WritePort handles CPU writes to ULA-controlled ports. Public
// entry point: dispatches to the internal handler and then fires
// the port tracer if one is installed.
func (u *ULA) WritePort(addr uint16, val byte) {
	u.writePortInternal(addr, val)
	if u.portTracer != nil {
		// Writes have no observable "handled" signal (the
		// internal dispatch swallows all addresses), so we always
		// report handled=true for writes. Reads have a real
		// handled flag from the underlying dispatcher.
		u.portTracer(addr, val, true /*write*/, true /*handled*/)
	}
}

// writePortInternal is the original WritePort body. It contains
// the early-return cascade for each port family. Kept as a
// separate function so the public WritePort can wrap it with
// tracing without disturbing the dispatch structure.
func (u *ULA) writePortInternal(addr uint16, val byte) {
	// Spectrum Next NextReg ports take priority over any other
	// dispatch when wired. 0x243B is the select latch (write-only),
	// 0x253B is the data port (read+write).
	if u.nextRegs != nil {
		switch addr {
		case 0x243B:
			u.nextRegs.Select(val)
			return
		case 0x253B:
			u.nextRegs.WriteData(val)
			return
		}
	}

	// Beta Disk / TR-DOS registers, while the TR-DOS ROM is paged in (see the
	// read side). Intercepts the FDC ports before the ULA/SpecDrum dispatch.
	if u.betaClaims(addr) {
		u.beta.WritePort(addr, val)
		return
	}

	// Ports $103B / $113B: Spectrum Next i2c SCL / SDA write latches
	// (zxnext.vhd:3234-3250 — bit 0 of the data byte drives the
	// open-drain line; full 16-bit decode $10xx/$11xx + $3B).
	if u.nextI2C != nil && (addr&0xFF) == 0x3B {
		switch addr >> 8 {
		case 0x10:
			u.nextI2C.WriteSCL(val&0x01 != 0)
			return
		case 0x11:
			u.nextI2C.WriteSDA(val&0x01 != 0)
			return
		}
	}

	// Port 0x6B: zxnDMA command stream. Decoded on low 8 bits only.
	if u.nextDMA != nil && (addr&0xFF) == 0x6B {
		u.nextDMA.WriteCommand(val)
		return
	}

	// Port $303B write: select the active sprite (mirror of NR$34).
	if u.nextSprite != nil && addr == 0x303B {
		u.nextSprite.SelectSprite(val)
		return
	}

	// Port 0x123B: legacy Spectrum Next Layer 2 control. Per the
	// TBBlue NextReg spec (nextreg.txt 0x69) "bit 7 = Enable layer
	// 2 (alias port 0x123B bit 1)" — boot.bin writes its testcard
	// to Layer 2 RAM and enables the layer via this port, NOT via
	// NR$69 directly. Without this dispatch the testcard centre
	// stays blank because Layer 2 is never visible to the
	// compositor. Bits beyond the visibility alias map to L2 write
	// enable / shadow / banking; they go through to NR$69 too so
	// the FPGA-canonical NextReg accurately reflects the state.
	if u.nextRegs != nil && addr == 0x123B {
		u.port123BVal = val // FPGA port_123b_dat — IN $123B reads this back
		nr69 := u.nextRegs.ReadReg(0x69)
		if val&0x02 != 0 {
			nr69 |= 0x80
		} else {
			nr69 &^= 0x80
		}
		if val&0x08 != 0 { // Shadow display alias bit 6
			nr69 |= 0x40
		} else {
			nr69 &^= 0x40
		}
		u.nextRegs.WriteReg(0x69, nr69)
		return
	}

	// Spectrum Next DAC ports (0x0F / 0x1F / 0xF1 / 0xF3 / 0xF9 /
	// 0xDF / 0xFB on the low byte). The bank returns true if the
	// port was a DAC channel — when handled, fall through to the
	// rest of the dispatch is unnecessary (DAC ports don't alias
	// classic ULA ports). When the port wasn't a DAC port the bank
	// returns false and we continue with the normal dispatch.
	if u.nextDAC != nil && u.nextDAC.WritePort(addr, val) {
		// Record the timed write so flushAudioFrame can reconstruct the DAC
		// waveform sample-accurately (event-timed, like the beeper).
		if u.audio != nil && u.mem.TStates != nil {
			if rec, ok := u.nextDAC.(interface{ Record(int) }); ok {
				rec.Record(int(*u.mem.TStates - u.frameStartTstate))
			}
		}
		return
	}

	// Classic-Spectrum SpecDrum ($DF) / Covox ($FB) DAC. When an enabled
	// device claims the port, latch the 8-bit sample with its T-state offset so
	// flushAudioFrame can reconstruct the waveform, and consume the write
	// (claiming $FB is why Covox and the ZX Printer can't both be on at once).
	if u.speccyDAC != nil && u.speccyDAC.Handles(byte(addr&0xFF)) {
		if u.audio != nil && u.mem.TStates != nil {
			u.speccyDAC.Record(int(*u.mem.TStates-u.frameStartTstate), val)
		}
		return
	}

	// divMMC control port 0xE3 (low-byte decode). The pager
	// claims the port if matched. NextZXOS's boot trampoline
	// writes 0 to 0xE3 to drop the divMMC overlay after it
	// finishes initialising; without this dispatch the boot
	// deadlocks in a tight 0x006A→0x1FF9→0x0001 loop.
	if u.nextDivMMC != nil && u.nextDivMMC.WritePort(addr, val) {
		return
	}

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
			if u.borderTracer != nil {
				u.borderTracer(addr, val, newBorder, scanline)
			}
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
	} else if u.nextAY != nil && (addr&0xC002) == 0xC000 && val >= 0xFD {
		// Spectrum Next TurboSound chip select: writing 0xFF/0xFE/0xFD to
		// port 0xFFFD selects AY chip 0/1/2 (chip = 0xFF - val). Register
		// selects are 0x00-0x0F, so there is no overlap. (NextReg 0x06 does
		// NOT select the chip.)
		u.nextAY.SelectChip(0xFF - val)
	} else if chip := u.activeAY(); chip != nil && (addr&0xC002) == 0xC000 {
		// AY-3-8912 register select: port 0xFFFD on 128K+ models.
		// Decoded as A15=1, A14=1, A1=0.
		chip.SelectRegister(val)
	} else if chip := u.activeAY(); chip != nil && (addr&0xC002) == 0x8000 {
		// AY-3-8912 data write: port 0xBFFD on 128K+ models.
		// Decoded as A15=1, A14=0, A1=0.
		chip.WriteSelected(val)
	} else if u.mem.GetCurrentModel() == roms.ModelPlus3 || u.mem.GetCurrentModel() == roms.ModelPlus2A || u.mem.GetCurrentModel() == roms.ModelNext {
		// +3 / +2A / Next use stricter port decoding to avoid
		// conflicts between 0x7FFD and 0x1FFD:
		//   0x7FFD: mask=0xC002 value=0x4000 (A15=0, A14=1, A1=0)
		//   0x1FFD: mask=0xF002 value=0x1000 (A15=0, A14=0, A13=0, A12=1, A1=0)
		// Without ModelNext in this branch, OUT (0x1FFD), val
		// fell through to the 0x7FFD case below (since 0x1FFD &
		// 0x8002 = 0 matches the loose 7FFD pattern) and remapped
		// slot 3 RAM bank — clobbering NextZXOS's stack
		// at 0xFFxx. NextZXOS then RETed into zero RAM, hit DI
		// at PC=0x0000, and boot deadlocked.
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

// applyNextCompositor walks the 192 active display rows, hands
// each one to the Spectrum Next compositor and writes the
// composited result back into u.img. Called from Render only
// when u.nextCompositor != nil.
//
// Cost: 192 rows × 256 pixels × {extract + compose + write} per
// frame. At 50 Hz that's a few hundred thousand pixel touches —
// well within budget per the §13.5 performance estimate. The
// allocations are pooled by stack-escape: ulaScan and composed
// fit comfortably below the 64K stack threshold.
func (u *ULA) applyNextCompositor() {
	const w = 256
	const h = 192
	// At 3.5 MHz the Copper runs ~one instruction per 4 CPU
	// T-states. A scanline is 228 T-states, so ~57 instructions
	// per scanline. Round to 64 for headroom; programs heavy on
	// WAITs typically execute far fewer.
	const copperInstrPerScanline = 64
	ulaScan := make([]byte, w*4)
	composed := make([]byte, w*4)
	for y := 0; y < h; y++ {
		// Tick the Copper BEFORE composing the row so MOVEs affecting
		// the compositor palette / Layer 2 are visible to this row's
		// composition (these layers ARE composited per-scanline here).
		//
		// A per-scanline ULA *inner-screen* refactor is NOT needed: that
		// content (u.img) is built from the fixed classic palette, screen
		// RAM, and the already-per-scanline border (port $FE) — none of
		// which a Copper NextReg MOVE can change — so there is no
		// copper-MOVE timing gap to close for it. (It would matter only
		// if the ULA honoured the copper-changeable Next ULA palette,
		// which it does not yet — a separate feature, not a timing bug.)
		if u.nextCopper != nil {
			// Step the Copper for scanline y up to the end-of-line
			// horizontal position (max hpos = 63) so WAITs targeting any
			// hpos on scanline y release on y, not one scanline late (the
			// previous hpos=0 only released hpos-0 WAITs). Identical for
			// the common hpos-0 WAITs, so the verified render is
			// unaffected; this is the achievable raster precision for a
			// per-scanline renderer (per-pixel hpos precision would need
			// per-pixel rendering).
			u.nextCopper.Step(uint16(y), 63, copperInstrPerScanline)
		}
		rowStart := (BorderTop+y)*u.img.Stride + BorderLeft*4
		copy(ulaScan, u.img.Pix[rowStart:rowStart+w*4])
		u.nextCompositor.ComposeScanline(y, ulaScan, composed)
		copy(u.img.Pix[rowStart:rowStart+w*4], composed)
	}

	// Border-area tilemap pass. Tilemap content in NextZXOS Browser
	// (40×32 tile grid = 320×256 pixels) extends beyond the classic
	// 256×192 inner screen into the 32-px L/R borders + 24-px T/B
	// borders. The inner pass above already painted tilemap inside
	// the 256×192 box; here we walk the FULL 320×240 image and only
	// touch border pixels.
	if u.nextCompositor.HasActiveTilemap() {
		rowFull := make([]byte, TotalWidth*4)
		for y := 0; y < TotalHeight; y++ {
			imgRowStart := y * u.img.Stride
			copy(rowFull, u.img.Pix[imgRowStart:imgRowStart+TotalWidth*4])
			// Tilemap y origin = top of image (y=0). The tilemap
			// itself is 256 lines tall; rows 0..239 of the image
			// map to tilemap rows 0..239 (the bottom 16 rows of
			// the 256-line tilemap are cropped out of the 240-line
			// image).
			inBorder := func(x int) bool {
				return x < BorderLeft || x >= BorderLeft+ScreenWidth
			}
			if y < BorderTop || y >= BorderTop+ScreenHeight {
				// Above or below the inner screen: every x is
				// border, paint the whole row.
				inBorder = func(int) bool { return true }
			}
			u.nextCompositor.ComposeBorderRow(y, rowFull, inBorder)
			copy(u.img.Pix[imgRowStart:imgRowStart+TotalWidth*4], rowFull)
		}
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
	// Prefer the Next's multi-chip AY engine when wired; otherwise the classic
	// single AY. (On the Next, SetNextAY usually runs after this and re-wires
	// it anyway, but handle the already-wired order too.)
	if u.nextAY != nil {
		u.audio.SetAY(u.nextAY)
	} else if u.ay != nil {
		u.audio.SetAY(u.ay)
	}
	// The Spectrum Next DAC (ModelNext) is mixed event-timed in flushAudioFrame
	// (see its GenerateFrame), so it is NOT wired into the audio system's
	// per-pull DACSource path here.
	if err := u.audio.Start(); err != nil {
		log.Printf("Warning: Failed to start audio system: %v", err)
	}
}

// SetPeripherals sets the peripheral manager for I/O port delegation
func (u *ULA) SetPeripherals(pm *peripherals.PeripheralManager) {
	u.peripherals = pm
}

// SetAudioKeepAliveLevel forwards a keep-alive dither level to the audio
// system (no-op if audio isn't enabled). See audio.SetKeepAliveLevel.
func (u *ULA) SetAudioKeepAliveLevel(level int16) {
	if u.audio != nil {
		u.audio.SetKeepAliveLevel(level)
	}
}

// SetDCBlockEnabled toggles the audio DC-blocking high-pass filter. Off emits
// the raw ±beeper levels (faithful squares, but the idle DC rail/click
// returns) — primarily an A/B diagnostic.
func (u *ULA) SetDCBlockEnabled(enabled bool) {
	u.dcEnabled = enabled
}

// SetFastLoad toggles fast-tape-turbo audio muting. While true, flushAudioFrame
// emits silence because the per-frame audio reconstruction is meaningless when
// dozens of emulated frames are collapsed into one audio frame.
func (u *ULA) SetFastLoad(on bool) {
	u.fastLoad = on
}

// FEReadCount returns the monotonic count of port-$FE reads. The fast-load
// turbo samples this per frame: a high read rate means the CPU is in a tape
// loader's edge-timing loop, a low rate means the game is running (only
// sparse keyboard reads), so turbo can stop once the program is live.
func (u *ULA) FEReadCount() uint64 {
	return u.feReadCount
}

// SetTapePlayer sets the tape player for tape loading. The tape clock is
// re-synced to the current CPU T-state so playback starts "now" rather than
// jumping forward by the whole elapsed run.
func (u *ULA) SetTapePlayer(tp *TapePlayer) {
	u.tape = tp
	if u.mem != nil && u.mem.TStates != nil {
		u.lastTapeTstate = *u.mem.TStates
	}
}

// tapeLevel advances the tape player to the current CPU T-state and returns the
// live EAR level. Called from every port-$FE read so edge-timed loaders (the
// ROM's LD-BYTES and games' custom turbo loaders alike) sample real pulses
// instead of a per-frame-frozen level. When no tape is loaded it's a cheap
// no-op returning the last level.
func (u *ULA) tapeLevel() bool {
	if u.tape == nil || u.mem == nil || u.mem.TStates == nil {
		return u.TapeIn
	}
	now := *u.mem.TStates
	prev := u.TapeIn
	playing := u.tape.IsPlaying()
	if now > u.lastTapeTstate && playing {
		u.TapeIn = u.tape.Update(now - u.lastTapeTstate)
	}
	u.lastTapeTstate = now
	// Record EAR transitions so flushAudioFrame can reproduce the loading sound.
	if u.audio != nil && playing && u.TapeIn != prev {
		if off := int(now - u.frameStartTstate); off >= 0 && off < 69888 {
			u.tapeAudioEvents = append(u.tapeAudioEvents, audioEvent{tstateOffset: off, state: u.TapeIn})
		}
	}
	return u.TapeIn
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
	u.tapeAudioEvents = u.tapeAudioEvents[:0]
	u.frameStartTapeState = false
	u.Speaker = false
	u.flash = false
	u.flashCount = 0
	u.KempstonState = 0
	// Clear any per-scanline border changes left in the buffer.
	// Without this, a model switch (e.g. 48K -> Next via the
	// Machine menu) inherits the previous model's border writes;
	// the next Render() then paints the stale colour bands as
	// horizontal stripes in the border before any new writes
	// happen. The drawn cells stay visible until the next Render
	// frame's clear at the end of the border-render block.
	u.borderChanges = u.borderChanges[:0]

	if u.audio != nil {
		u.audio.Reset()
	}
	// Re-arm the DC blocker so the first post-reset frame establishes a fresh
	// silent baseline (the audio queue is re-primed with silence too). This is
	// what stops the reset itself (e.g. a +3 disk boot) from clicking.
	u.dc.reset()

	// Sync the AY presence with the current memory model. SwitchModel may
	// have changed the machine since the ULA was created, so we (re)create
	// the AY here for any 128K+ model and detach it on a plain 48K.
	if u.mem.GetCurrentModel() != roms.Model48K {
		if u.ay == nil {
			u.ay = ay.New()
		} else {
			u.ay.Reset()
		}
		if u.nextAY != nil {
			u.nextAY.Reset() // reset all TurboSound chips (incl. chip 0 == u.ay)
		}
		// Keep the mixer pointed at the engine on the Next (chip 0 == u.ay), or
		// the single AY otherwise — so AY music survives a reset/reboot.
		if u.audio != nil {
			if u.nextAY != nil {
				u.audio.SetAY(u.nextAY)
			} else {
				u.audio.SetAY(u.ay)
			}
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
	// During fast-tape turbo, many emulated frames collapse into this single
	// audio frame, so the reconstructed waveform is garbled. Emit silence and
	// re-arm the DC blocker so normal audio resumes cleanly once loading ends.
	if u.fastLoad {
		u.audioEvents = u.audioEvents[:0]
		u.tapeAudioEvents = u.tapeAudioEvents[:0]
		u.frameStartTapeState = false
		u.frameStartSpeakerState = u.Speaker
		u.dc.reset()
		u.audio.PushBeeperSamples(make([]int16, audio.SamplesPerFrame))
		if u.mem.TStates != nil {
			u.frameStartTstate = *u.mem.TStates
		}
		return
	}
	samples, finalState := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)
	// Mix the SpecDrum/Covox DAC frame (event-timed, sample-accurate) into the
	// beeper waveform before pushing it.
	if u.speccyDAC != nil && u.speccyDAC.Enabled() {
		const tstatesPerFrame = 69888
		mixInt16(samples, u.speccyDAC.GenerateFrame(audio.SamplesPerFrame, tstatesPerFrame))
	}
	// Spectrum Next 4-channel DAC: event-timed, mixed the same way (replaces the
	// old per-pull MixInto snapshot).
	if gen, ok := u.nextDAC.(interface {
		GenerateFrame(int, int) []int16
	}); ok && gen != nil {
		const tstatesPerFrame = 69888
		mixInt16(samples, gen.GenerateFrame(audio.SamplesPerFrame, tstatesPerFrame))
	}
	// Tape-loading sound: reconstruct the EAR waveform and mix it in (the
	// audible pilot whistle + data screech). Only while a tape is playing, so
	// there's no DC bias once loading finishes.
	if u.tape != nil && u.tape.IsPlaying() {
		tapeSamples, finalTape := generateSquareWaveFrame(
			u.tapeAudioEvents, u.frameStartTapeState, -tapeAudioAmplitude, tapeAudioAmplitude)
		mixInt16(samples, tapeSamples)
		u.frameStartTapeState = finalTape
	} else {
		u.frameStartTapeState = false
	}
	u.tapeAudioEvents = u.tapeAudioEvents[:0]

	// AC-couple the mix (beeper + tape + DAC) like the hardware's output
	// capacitor: a held level decays to silence and only edges make sound, so
	// idle/power-on/reset and the gaps between loader blocks no longer step
	// to a full-scale DC rail (the "battery click").
	if u.dcEnabled {
		u.dc.process(samples)
	}

	u.audio.PushBeeperSamples(samples)
	u.frameStartSpeakerState = finalState
	u.audioEvents = u.audioEvents[:0]
	if u.mem.TStates != nil {
		u.frameStartTstate = *u.mem.TStates
	}
}

// mixInt16 adds src into dst element-wise with int16 saturation. Used to fold
// the DAC frame into the beeper frame without wrap-around pops.
func mixInt16(dst, src []int16) {
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := 0; i < n; i++ {
		sum := int32(dst[i]) + int32(src[i])
		switch {
		case sum > 32767:
			dst[i] = 32767
		case sum < -32768:
			dst[i] = -32768
		default:
			dst[i] = int16(sum)
		}
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
	return generateSquareWaveFrame(events, initialState, beeperLow, beeperHigh)
}

// generateSquareWaveFrame is the box-filter square-wave reconstruction shared by
// the beeper and the tape-loading sound: it integrates a 1-bit signal (toggled
// by `events`) into one frame of samples between `low` (state false) and `high`
// (state true). See generateBeeperFrame for why integration (not point-sampling)
// is used.
func generateSquareWaveFrame(events []audioEvent, initialState bool, low, high int16) (samples []int16, finalState bool) {
	const tstatesPerFrame = 69888
	samples = make([]int16, audio.SamplesPerFrame)
	state := initialState
	eventIdx := 0

	delta := int32(high) - int32(low)
	lowV := int32(low)

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
			samples[i] = int16(lowV + delta*int32(highTstates)/int32(sampleLen))
		} else {
			samples[i] = low
		}
	}
	return samples, state
}

// Beeper amplitude levels — symmetric around zero. The 1-bit speaker is
// rendered at ±beeperHigh and the per-frame mix is then DC-blocked (see
// dcBlocker) to model the real Spectrum's capacitor-coupled output, so an
// idle level decays to silence instead of sitting at a full-scale rail.
//
// The amplitude is capped so that a *full swing* (beeperLow→beeperHigh =
// 2·beeperHigh = 32000) stays inside int16: the DC blocker's step response
// is the swing height, so an isolated speaker toggle renders as a clean
// 32000 transient rather than a clipped 40000 spike. The remaining headroom
// (32767 − 16000) also covers one AY channel at max without clipping; the
// worst case (3 AY channels + beeper at peak) is rare and clips gracefully
// via the int32 saturation in MixInto.
const (
	beeperHigh int16 = 16000
	beeperLow  int16 = -16000

	// tapeAudioAmplitude is the peak level of the mixed-in tape-loading sound.
	// Below the beeper so it's clearly the loading tone, not deafening, and
	// leaves headroom for the beeper/AY in the saturating mix.
	tapeAudioAmplitude int16 = 9000
)
