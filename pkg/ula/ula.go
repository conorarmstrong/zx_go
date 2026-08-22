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

// TStatesPerLine is the number of T-states per scanline. 228 is the 128K
// family value (456 video columns / 2). The 48K ULA uses 224 (448 / 2); see
// TStatesPerLineFor. This default is retained for the 128K-anchored callers
// (BeamPosition / ActiveVideoLine on the Next, which boots in 128K timing).
const TStatesPerLine = 228

// LinesPerFrame is the scanline count of a 128K-family frame (311 lines of
// 228 T-states = 70908). The raster counters wrap here, as the FPGA's vc does
// at c_max_vc — a line number outside this range is not a raster position.
const LinesPerFrame = 311

// copperSegmentPixels is the width of one compositing segment. It is the
// Copper's own horizontal resolution: its WAIT column field is 6 bits taken
// as 8-pixel units (device/copper.vhd:94), so stepping and composing at this
// granularity is exact rather than an approximation.
const copperSegmentPixels = 8

// TStatesPerLineFor returns the documented T-states-per-scanline for a machine
// model: 224 for the 48K (312 lines * 224 = 69888 T-states/frame), 228 for the
// 128K family and +2/+2A/+3 (311 lines * 228 = 70908). The Spectrum Next boots
// in 128K/+3 timing. Matches video/zxula_timing.vhd c_max_hc: 48K=447 (448
// columns → 224 T) and 128K=455 (456 columns → 228 T), and Sean Young's /
// Chris Smith's classic timing references.
func TStatesPerLineFor(model roms.SpectrumModel) int {
	if model == roms.Model48K {
		return 224
	}
	return 228
}

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
	// nextFullImg is the 320×256 over-border frame for the Next: the standard
	// 320×240 image plus the 8-px top/bottom strips of the sprite frame (Y 0-7
	// and 248-255) that the classic 24-px border crops. Reused across frames.
	// Built only when the Next sprite layer is active (NextBASIC Invaders parks
	// its player ship at sprite Y=240, in the bottom over-border strip).
	nextFullImg *image.RGBA
	// compositorScan / compositorComposed are reused across frames as the
	// per-row scratch buffers for the Spectrum Next inner-screen compositor
	// pass (applyNextCompositor), and compositorRow likewise for its
	// border-area tilemap and sprite passes (run sequentially, so the one
	// buffer serves both), avoiding a heap allocation on every row pass of
	// every frame.
	compositorScan     []byte
	compositorComposed []byte
	compositorRow      []byte
	palette            [16]color.RGBA
	// borderTracer, if non-nil, fires on every border-colour change
	// caused by an even-port write. Used by the debugger to observe
	// border modulation through any port that matches the ULA's
	// "even-address" decode (not just $FE), which a port-tracer
	// keyed by port number can miss.
	borderTracer func(port uint16, val byte, newBorder byte, scanline int)
	flash        bool
	flashCount   int

	// timexVideoMode is the last value written to the Timex SCLD register
	// (port $FF): bits 2:0 = display mode (110 = 512x192 8x1 hi-res), bits 5:3
	// = hi-res ink/paper colour. 0 (the reset default) is the normal screen.
	timexVideoMode byte

	// timexModeObserver is notified on every port $FF write. nil off the Next.
	timexModeObserver func(mode byte)

	// nextULAPlus handles ports $BF3B / $FF3B. nil off the Next.
	nextULAPlus NextULAPlus

	// nextLoRes replaces the ULA bitmap when the Next's LoRes/Radastan mode is
	// on. nil off the Next. loresRow is its per-row scratch, hoisted so 192
	// rows a frame do not churn the allocator.
	nextLoRes NextLoRes
	loresRow  [ScreenWidth]color.RGBA

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

	// ulaOutputDisabled mirrors NextReg $68 bit 7 ("Disable ULA output").
	// When set the ULA layer paints nothing — the screen area shows the
	// lower layers (Layer 2 / Tilemap) or the NR$4A fallback colour, never
	// stale screen RAM. Sonic disables the ULA for its Layer-2/tilemap
	// title; without honouring this, stale screen RAM rendered as garbage.
	ulaOutputDisabled bool

	// ULA scroll (NextReg 0x26 / 0x27) and the NR$68 bit 2 half-pixel
	// refinement. See ulascroll.go.
	ulaScrollX, ulaScrollY byte
	ulaFineScrollX         bool

	// Mid-frame border tracking: records (scanline, colour) pairs for each border change.
	// Allows accurate rendering of border effects that change colour during the frame.
	borderChanges []borderChange
	// frameStartBorderColour is the border colour in effect at the start of
	// the frame currently being built, i.e. before any of this frame's port
	// 0xFE writes. BorderColour itself is mutated live by WritePort as the
	// CPU runs, so by the time Render() runs it already holds this frame's
	// latest value — Render uses frameStartBorderColour, not BorderColour,
	// as the baseline for scanlines before the first recorded change.
	frameStartBorderColour byte

	// Beeper audio event recording. Each port-0xFE write that flips
	// bit 4 appends an (offset, state) tuple here. Render() walks the
	// list at end of frame to synthesise audio samples and pushes
	// them to the audio system. Reset at start of every frame.
	audioEvents      []audioEvent
	frameStartTstate uint64

	// hasRendered records whether a frame has ever been composed, so
	// LastFrame can compose one lazily instead of returning a blank image.
	hasRendered bool
	// lastImg is the frame Render most recently returned, which is not always
	// u.img: the 80-column and hi-res paths return their own wider buffers.
	lastImg                *image.RGBA
	frameStartSpeakerState bool

	// dc models the capacitor-coupled audio output: it high-pass-filters the
	// per-frame mix so a held speaker level decays to silence instead of
	// sitting at a full-scale DC rail (which made power-on/reset/tape
	// boundaries click like a speaker wired to a battery). dcEnabled allows
	// disabling it (A/B diagnostics) — when off, the raw ±beeper levels are
	// emitted (faithful square waves, but the idle DC rail/click returns).
	dc        audio.StereoDCBlocker
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

	// nextRasterLog, when wired, replays this frame's CPU writes to the
	// Next's visual state at the row each was made on (see NextRasterLog).
	nextRasterLog NextRasterLog

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
// the compositor needs to pull in more pkg/ula state, e.g. for a
// sprite bandwidth model).
type NextCompositor interface {
	ComposeScanline(y int, ulaRGBA []byte, dst []byte)
	// ComposeScanlineRange composes pixels [x0, x1) of a row, so the caller
	// can interleave Copper steps with compositing across the line.
	ComposeScanlineRange(y int, ulaRGBA []byte, dst []byte, x0, x1 int)
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
	// HasActiveSprites reports whether the sprite layer is wired AND
	// enabled, so the ULA knows whether to run the sprite border pass.
	HasActiveSprites() bool
	// ComposeSpriteBorderRow paints sprite pixels over the border-area
	// pixels of a 320-wide RGBA row. frameY is the sprite vcounter for
	// this row (frame-relative); isInBorderArea(x) selects the pixels to
	// paint, leaving inner-screen pixels to the main pass.
	ComposeSpriteBorderRow(frameY int, dst []byte, isInBorderArea func(x int) bool)
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
	// SelectSlot applies a port $303B write: sets the current sprite and the
	// pattern-RAM upload cursor (ports.txt 0x303B).
	SelectSlot(v byte)
	// WritePatternByte streams one byte to the current sprite-pattern cursor
	// (port $005B, auto-incrementing).
	WritePatternByte(v byte)
	// WriteAttr streams one byte to the current sprite's attributes (port
	// $0057); after a sprite's 4/5 bytes the current-sprite pointer advances.
	WriteAttr(v byte)
	ReadStatus() byte
}

// NextDMA is the contract for the zxnDMA ports $6B and $0B (ports.txt: the
// accessing port selects the DMA mode). pkg/next/dma.DMA satisfies it:
// WriteCommand consumes the WR-register byte stream; ReadCommand returns the
// next register in the read-mask sequence; SetZ80Mode latches the mode on
// every access — false for $6B (zxn dma), true for $0B (Z80-DMA compatible),
// mirroring zxnext.vhd:1817 (dma_mode <= port_0b_lsb on any DMA rd/wr).
type NextDMA interface {
	WriteCommand(val byte)
	ReadCommand() byte
	SetZ80Mode(on bool)
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
	Step(scanline uint16, hcount uint16, maxInstr int) int
}

// NextRasterLog is the contract for replaying mid-frame CPU writes to the
// Next's visual state at the row they were made on. pkg/next/rasterlog.Log
// satisfies it.
//
// The compositor pass rewinds to the frame-start state, walks down applying
// each change as it reaches its row, then applies the remainder so the next
// frame starts from the live state. Without it every CPU write applied
// retroactively to the whole frame.
type NextRasterLog interface {
	BeginReplay()
	ApplyThrough(row int)
	EndReplay()
	Len() int
}

// NextDAC is the contract for the four Spectrum Next DAC channels.
// pkg/next/dac.Bank satisfies it via WritePort (which returns
// "handled?" so the ULA's port dispatcher knows whether to fall
// through). The ULA forwards every port write to the DAC; the bank
// internally checks the low byte for one of the documented DAC
// ports and ignores everything else.
// The frame methods are part of the contract rather than something the ULA
// discovers with a runtime type assertion. They used to be discovered, and it
// cost a silent regression: renaming GenerateFrame to GenerateFrameStereo made
// the assertion stop matching, which disconnected the DAC from the audio path
// with no build error and no failing test. An interface method cannot fail that
// way.
type NextDAC interface {
	WritePort(port uint16, val byte) bool
	// Record timestamps the write that just happened, so the frame can be
	// reconstructed at sample accuracy rather than snapshotted per pull.
	Record(tstateOffset int)
	// GenerateFrameStereo returns one frame of interleaved stereo samples,
	// 2*samplesPerFrame values, and clears the recorded events.
	GenerateFrameStereo(samplesPerFrame, tstatesPerFrame int) []int16
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

// SetNextRasterLog installs the mid-frame raster journal. Passing nil (the
// default, and every classic model) leaves the compositor pass exactly as it
// was. See NextRasterLog.
func (u *ULA) SetNextRasterLog(l NextRasterLog) { u.nextRasterLog = l }

// FrameID identifies the frame currently being executed, as the running
// T-state counter divided by the model's frame length.
//
// Derived from the clock rather than from the ULA's own frame bookkeeping on
// purpose: that bookkeeping only advances when a frame is actually rendered,
// and the raster journal has to be scoped correctly on the paths that execute
// frames WITHOUT rendering — headless stepping, fast tape turbo, the
// debugger, RZX playback. Those are exactly the paths where an unscoped
// journal grows without bound.
func (u *ULA) FrameID() uint64 {
	if u.mem == nil || u.mem.TStates == nil {
		return 0
	}
	frame := roms.FrameTStates(u.mem.GetCurrentModel())
	if frame <= 0 {
		return 0
	}
	return *u.mem.TStates / uint64(frame)
}

// NextRasterLogForTest exposes the installed raster journal so an
// integration test can drive a rewind/replay directly. Returns nil when none
// is wired.
func (u *ULA) NextRasterLogForTest() NextRasterLog { return u.nextRasterLog }

// DisplayRow reports which display row the raster is on right now: 0..191
// inside the active area, rasterlog.RowBeforeDisplay (-1) during the top
// border or vblank, and rasterlog.RowAfterDisplay (192) below the display.
// Used to timestamp journalled writes.
func (u *ULA) DisplayRow() int {
	if u.mem == nil || u.mem.TStates == nil {
		return -1
	}
	model := u.mem.GetCurrentModel()
	t := int(*u.mem.TStates - u.frameStartTstate)
	start := roms.DisplayStartTState(model)
	if t < start {
		return -1
	}
	row := (t - start) / TStatesPerLineFor(model)
	if row >= ScreenHeight {
		return ScreenHeight
	}
	return row
}

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
	u.dc.SetLimit(int32(beeperHigh))
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

// SetULAOutputDisabled mirrors NextReg $68 bit 7. When true the ULA layer is
// not painted (see Render). Idempotent and safe to call every frame.
func (u *ULA) SetULAOutputDisabled(disabled bool) { u.ulaOutputDisabled = disabled }

// ulaDisabledFill is the colour painted across the frame when the ULA output
// is disabled: the Next compositor's NR$4A fallback when one is wired, else
// opaque black.
func (u *ULA) ulaDisabledFill() color.RGBA {
	if fb, ok := u.nextCompositor.(interface{ FallbackRGBA() [4]byte }); ok {
		c := fb.FallbackRGBA()
		return color.RGBA{R: c[0], G: c[1], B: c[2], A: 0xFF}
	}
	return color.RGBA{A: 0xFF}
}

// LastFrame returns the most recently rendered frame WITHOUT composing a new
// one.
//
// Use this to look at the screen — screenshots, measurements, debugger views.
// Render() is not an observation: on the Next its compose walk steps the
// Copper as it goes, because a MOVE has to affect the segments after it within
// the same frame. Calling Render() again for a frame therefore runs the Copper
// program a second time and leaves the visual registers somewhere the machine
// never was. Measured on TX-1696: the first render produced its title screen
// in 20 colours, and every render after it a black frame, from identical state
// with no CPU time in between.
//
// Render() stays what it is — "advance the picture by one frame" — and callers
// that merely want to see it use this instead.
// It composes once, lazily, if nothing has been rendered yet — so asking for
// the screen before the machine has run still gives a picture rather than a
// blank one.
func (u *ULA) LastFrame() *image.RGBA {
	if !u.hasRendered || u.lastImg == nil {
		return u.Render()
	}
	return u.lastImg
}

// Render composes the next frame and returns it.
//
// Call it ONCE per emulated frame. It is not idempotent: see LastFrame.
func (u *ULA) Render() *image.RGBA {
	img := u.render()
	// Remember exactly what was returned. Render has several exits — the
	// 320-pixel base, the 640-pixel 80-column frame, the hi-res Layer 2 frame
	// — and LastFrame must hand back the one the machine is actually showing,
	// not the base it was built from.
	u.lastImg = img
	u.hasRendered = true
	return img
}

func (u *ULA) render() *image.RGBA {
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
		// Start with the colour that was active before the first change in
		// this frame (frameStartBorderColour, not the live BorderColour,
		// which this frame's writes have already advanced past).
		currentBorder := u.frameStartBorderColour
		if u.borderChanges[0].scanline == 0 {
			currentBorder = u.borderChanges[0].colour
		}
		changeIdx := 0
		// The frame scanline the display starts on. Only the 48K's is a whole
		// 64 lines from the interrupt — see roms.DisplayStartTState — so
		// hardcoding 64 put a mid-frame border change on the wrong row by
		// about a scanline for the whole 128K family.
		displayStartLine := roms.DisplayStartTState(u.mem.GetCurrentModel()) /
			TStatesPerLineFor(u.mem.GetCurrentModel())
		for line := 0; line < TotalHeight; line++ {
			// Advance past any border changes that apply to this scanline.
			// Image row 0 is BorderTop rows above the first display row.
			frameScanline := line + (displayStartLine - BorderTop)
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
	// Snapshot the now-current border colour as the baseline for the next
	// frame's render (see frameStartBorderColour).
	u.frameStartBorderColour = u.BorderColour

	// NextReg $68 bit 7 ("Disable ULA output"): the ULA layer paints
	// nothing. Fill the whole frame with the disabled fill (the NR$4A
	// fallback colour when a Next compositor is wired, else black) so the
	// border + screen passes are skipped and the lower layers / fallback
	// show instead of stale screen RAM. This makes the ULA fully
	// transparent regardless of NR$14 (which sonic sets >= 16, disabling
	// the per-pixel transparency path).
	if u.ulaOutputDisabled {
		fill := u.ulaDisabledFill()
		for y := 0; y < TotalHeight; y++ {
			for x := 0; x < TotalWidth; x++ {
				u.img.Set(x, y, fill)
			}
		}
		if u.nextCompositor != nil {
			u.applyNextCompositor()
			if u.nextCompositor.HiResLayer2Active() {
				return u.renderHiResLayer2()
			}
			if u.nextCompositor.TilemapIs80Col() {
				return u.renderWide()
			}
		}
		return u.img
	}

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

	// NR$26 / NR$27 ULA scroll. Both are zero on every classic model and at
	// Next reset, in which case srcY == y and the column offset is 0, so this
	// collapses to the unscrolled fetch. See ulascroll.go.
	scrollChars, scrollPixels := ulaScrollColumn(u.ulaScrollX)

	for y := 0; y < ScreenHeight; y++ {
		srcY := ulaScrollRow(y, u.ulaScrollY)
		for x := 0; x < ScreenWidth/8; x++ {
			// The pixel shift straddles two character cells, so fetch this
			// one and its neighbour and combine (zxula.vhd fetches px and
			// px_1 for exactly this).
			col := (x + scrollChars) & 31
			addr := screenAddrForRowCol(srcY, col)
			attrAddr := ((srcY >> 3) * 32) + col

			pixels := screenMem[addr]
			if scrollPixels != 0 {
				next := screenMem[screenAddrForRowCol(srcY, (col+1)&31)]
				pixels = pixels<<uint(scrollPixels) | next>>uint(8-scrollPixels)
			}
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

	// Spectrum Next LoRes / Radastan: this REPLACES the ULA bitmap rather than
	// compositing over it, so it runs before every other Next layer and hands
	// them a substituted ULA row:
	//
	//	ulalores_pixel_1 <= lores_pixel_1 when lores_pixel_en_1 = '1'
	//	                    else ula_pixel_1;          -- zxnext.vhd:6980
	//
	// Guarded on Active, which is NR$15 bit 7: with LoRes off this costs one
	// nil check per frame and the picture is byte-identical to before the
	// layer existed.
	u.applyNextLoRes()

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

	// Timex 512x192 8x1 hi-res (port $FF mode 110): the NextZXOS 64/85-column
	// text modes (e.g. the .more text viewer) use it. Rendered as a 640-wide
	// frame, like the other wide modes.
	if u.timexHiResActive() {
		return u.renderTimexHiRes()
	}

	// Next over-border: the sprite frame is 320×256 (32-px top/bottom borders),
	// but the classic frame is 320×240 (24-px). When the Next sprite layer is
	// active, return the full 256-line frame so sprites in the top/bottom
	// over-border strips (e.g. NBI's player ship at sprite Y=240) are visible
	// instead of cropped. (Classic models have no compositor and are unaffected.)
	if u.nextCompositor != nil && u.nextCompositor.HasActiveSprites() {
		return u.renderNextFullHeight()
	}

	return u.img
}

// renderNextFullHeight returns the 320×256 over-border Next frame: the standard
// 320×240 render copied into the centre (rows 8..247 = sprite frame Y 8..247)
// plus the two 8-px strips the classic border crops — the top (frame Y 0..7)
// and bottom (frame Y 248..255). Each strip is filled with the border colour
// then has the over-border sprite pass run over it, so sprites parked in the
// Next's extra border band render fully. In this 256-line image the row index
// equals the sprite frame Y (bias 0), matching applyNextCompositor's y+8 map
// for the copied middle.
func (u *ULA) renderNextFullHeight() *image.RGBA {
	const fullH = 256
	const extra = (fullH - TotalHeight) / 2 // 8 px added top and bottom
	if u.nextFullImg == nil {
		u.nextFullImg = image.NewRGBA(image.Rect(0, 0, TotalWidth, fullH))
	}
	dst := u.nextFullImg
	// Middle band: copy the 240-line render into rows extra..extra+TotalHeight-1.
	copy(dst.Pix[extra*dst.Stride:(extra+TotalHeight)*dst.Stride], u.img.Pix[:TotalHeight*u.img.Stride])
	// Over-border strips: border fill + the sprite border pass (whole row is
	// border in these strips). frameY == dst row here (bias 0).
	bc := u.palette[u.BorderColour&0x0F]
	rowFull := make([]byte, TotalWidth*4)
	allBorder := func(int) bool { return true }
	paintStrip := func(rowStart, rowEnd int) {
		for fy := rowStart; fy < rowEnd; fy++ {
			for x := 0; x < TotalWidth; x++ {
				o := x * 4
				rowFull[o], rowFull[o+1], rowFull[o+2], rowFull[o+3] = bc.R, bc.G, bc.B, 0xFF
			}
			u.nextCompositor.ComposeSpriteBorderRow(fy, rowFull, allBorder)
			copy(dst.Pix[fy*dst.Stride:fy*dst.Stride+TotalWidth*4], rowFull)
		}
	}
	paintStrip(0, extra)           // top over-border: frame Y 0..7
	paintStrip(fullH-extra, fullH) // bottom over-border: frame Y 248..255
	return dst
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
	// NextReg $68 bit 2, the ULA half-pixel horizontal scroll. zxula.vhd:353
	// builds the shift as `px(2 downto 0) & px(8)`, a 4-bit count in HALF
	// pixels, so the low bit moves the picture by half of one ULA pixel.
	//
	// At the 320-pixel width that is not representable and the bit can only be
	// stored. Here it is: each ULA pixel occupies two units, so half a pixel is
	// exactly one of them. The whole-pixel part of the scroll is already
	// applied during the fetch (see ulascroll.go); this is only the remainder.
	fine := 0
	if u.ulaFineScrollX {
		fine = 1
	}
	for y := 0; y < TotalHeight; y++ {
		srcStart := y * u.img.Stride
		for x := 0; x < TotalWidth; x++ {
			s := srcStart + x*4
			r, g, b, a := u.img.Pix[s+0], u.img.Pix[s+1], u.img.Pix[s+2], u.img.Pix[s+3]
			d := x*8 - fine*4
			if d < 0 {
				// The half-unit shifted off the left edge; its partner unit
				// still lands, so only the very first sample is dropped.
				rowWide[0], rowWide[1], rowWide[2], rowWide[3] = r, g, b, a
				continue
			}
			rowWide[d+0], rowWide[d+1], rowWide[d+2], rowWide[d+3] = r, g, b, a
			if d+7 < len(rowWide) {
				rowWide[d+4], rowWide[d+5], rowWide[d+6], rowWide[d+7] = r, g, b, a
			}
		}
		u.nextCompositor.ComposeWideTilemapRow(y, rowWide)
		dstStart := y * wide.Stride
		copy(wide.Pix[dstStart:dstStart+ww*4], rowWide)
	}
	return wide
}

// timexHiResActive reports whether the Timex SCLD register (port $FF) selects
// the 512x192 8x1 hi-res display mode (bits 2:0 == 110).
func (u *ULA) timexHiResActive() bool { return u.timexVideoMode&0x07 == 0x06 }

// TimexScreenMode returns the last value written to port $FF, the Timex SCLD
// video-mode register.
func (u *ULA) TimexScreenMode() byte { return u.timexVideoMode }

// SetTimexModeObserver installs a callback fired on every port $FF write.
func (u *ULA) SetTimexModeObserver(fn func(mode byte)) { u.timexModeObserver = fn }

// NextULAPlus is the contract for the Spectrum Next's ULA+ register-select and
// data ports. pkg/next.ULAPlus satisfies it; the interface lives here so this
// package does not import pkg/next.
type NextULAPlus interface {
	WriteBF3B(v byte)
	// WriteFF3B reports whether it consumed the write; false falls through.
	WriteFF3B(v byte) bool
	ReadFF3B() (byte, bool)
}

// SetNextULAPlus attaches the ULA+ port handler. nil unhooks.
func (u *ULA) SetNextULAPlus(p NextULAPlus) { u.nextULAPlus = p }

// NextLoRes is the contract for the Spectrum Next's LoRes / Radastan layer.
// pkg/next/compositor.LoRes satisfies it; the interface lives here so this
// package does not import that one.
//
// It is deliberately NOT part of NextCompositor. LoRes is not a layer in the
// mixer: it substitutes for the ULA's own pixels before the mixer runs, so it
// belongs at a different point in the frame and has a different contract.
type NextLoRes interface {
	// Active reports whether the layer would claim any pixel at all.
	Active() bool
	// ComposeULARow replaces row's pixels for paper row y wherever the layer's
	// clip window admits them, leaving the rest as they arrived.
	ComposeULARow(y int, row []color.RGBA)
}

// SetNextLoRes attaches the LoRes / Radastan layer. nil unhooks.
func (u *ULA) SetNextLoRes(l NextLoRes) { u.nextLoRes = l }

// applyNextLoRes substitutes the LoRes picture for the ULA bitmap in the
// classic 256x192 paper area. The border is untouched: lores.vhd's clip window
// is expressed in the same raster coordinates as the paper, and the FPGA's
// substitution happens at the ULA pixel, which the border is not.
func (u *ULA) applyNextLoRes() {
	if u.nextLoRes == nil || !u.nextLoRes.Active() {
		return
	}
	for y := 0; y < ScreenHeight; y++ {
		row := u.loresRow[:]
		for x := 0; x < ScreenWidth; x++ {
			r, g, b, a := u.img.At(BorderLeft+x, BorderTop+y).RGBA()
			row[x] = color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
		}
		u.nextLoRes.ComposeULARow(y, row)
		for x := 0; x < ScreenWidth; x++ {
			u.img.Set(BorderLeft+x, BorderTop+y, row[x])
		}
	}
}

// timexHiResColours decodes the hi-res ink/paper from port $FF bits 5:3. Hi-res
// uses two bright, complementary colours: ink = colour code, paper = 7 - code
// (so code 0 = black ink on white paper, the default text colours).
func (u *ULA) timexHiResColours() (ink, paper color.RGBA) {
	code := (u.timexVideoMode >> 3) & 0x07
	return u.palette[code|0x08], u.palette[(7-code)|0x08]
}

// renderTimexHiRes builds a 640×TotalHeight frame for the Timex 512×192 8x1
// hi-res mode. The pixel-doubled base frame supplies the (doubled) border; the
// central 512px paper is drawn at native resolution from the two display files
// — display file 1 (screen base) provides the even byte columns, display file 2
// (base + $2000) the odd — interleaved, with the y-address scramble of the
// standard screen. This is how the Next runs its 64/85-column text at double
// the horizontal pixel clock.
func (u *ULA) renderTimexHiRes() *image.RGBA {
	const ww = 2 * TotalWidth // 640
	if u.wideImg == nil {
		u.wideImg = image.NewRGBA(image.Rect(0, 0, ww, TotalHeight))
		u.wideRow = make([]byte, ww*4)
	}
	wide := u.wideImg
	// Pixel-double the base frame (correct doubled border + a fallback paper).
	for y := 0; y < TotalHeight; y++ {
		srcStart := y * u.img.Stride
		dstStart := y * wide.Stride
		for x := 0; x < TotalWidth; x++ {
			s := srcStart + x*4
			r, g, b, a := u.img.Pix[s+0], u.img.Pix[s+1], u.img.Pix[s+2], u.img.Pix[s+3]
			d := dstStart + x*8
			wide.Pix[d+0], wide.Pix[d+1], wide.Pix[d+2], wide.Pix[d+3] = r, g, b, a
			wide.Pix[d+4], wide.Pix[d+5], wide.Pix[d+6], wide.Pix[d+7] = r, g, b, a
		}
	}
	screen := u.mem.GetPage(u.mem.ScreenPage)
	if len(screen) < 0x2000+6144 {
		return wide
	}
	ink, paper := u.timexHiResColours()
	for sy := 0; sy < ScreenHeight; sy++ { // 192
		py := BorderTop + sy
		for fileIdx := 0; fileIdx < ScreenWidth/8; fileIdx++ { // 0..31
			addr := screenAddrForRowCol(sy, fileIdx)
			for half := 0; half < 2; half++ {
				bb := screen[addr] // display file 1 -> even display bytes
				if half == 1 {
					bb = screen[0x2000+addr] // display file 2 -> odd display bytes
				}
				dpByte := 2*fileIdx + half // 0..63
				for bit := 0; bit < 8; bit++ {
					px := 2*BorderLeft + dpByte*8 + bit // paper starts at x=64
					col := paper
					if bb&(0x80>>bit) != 0 {
						col = ink
					}
					d := py*wide.Stride + px*4
					wide.Pix[d+0], wide.Pix[d+1], wide.Pix[d+2], wide.Pix[d+3] = col.R, col.G, col.B, 0xFF
				}
			}
		}
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
	// NextReg $68 bit 2, the ULA half-pixel horizontal scroll. zxula.vhd:353
	// builds the shift as `px(2 downto 0) & px(8)`, a 4-bit count in HALF
	// pixels, so the low bit moves the picture by half of one ULA pixel.
	//
	// At the 320-pixel width that is not representable and the bit can only be
	// stored. Here it is: each ULA pixel occupies two units, so half a pixel is
	// exactly one of them. The whole-pixel part of the scroll is already
	// applied during the fetch (see ulascroll.go); this is only the remainder.
	fine := 0
	if u.ulaFineScrollX {
		fine = 1
	}
	for y := 0; y < TotalHeight; y++ {
		srcStart := y * u.img.Stride
		for x := 0; x < TotalWidth; x++ {
			s := srcStart + x*4
			r, g, b, a := u.img.Pix[s+0], u.img.Pix[s+1], u.img.Pix[s+2], u.img.Pix[s+3]
			d := x*8 - fine*4
			if d < 0 {
				// The half-unit shifted off the left edge; its partner unit
				// still lands, so only the very first sample is dropped.
				rowWide[0], rowWide[1], rowWide[2], rowWide[3] = r, g, b, a
				continue
			}
			rowWide[d+0], rowWide[d+1], rowWide[d+2], rowWide[d+3] = r, g, b, a
			if d+7 < len(rowWide) {
				rowWide[d+4], rowWide[d+5], rowWide[d+6], rowWide[d+7] = r, g, b, a
			}
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
	// Wrap at the frame, not at 9 bits. The old `& 0x1FF` bounded the line at
	// 511, which is not a raster position: a frame is LinesPerFrame lines, and
	// software polling NextReg $1E/$1F for a scanline is comparing against a
	// number that must exist.
	//
	// Wrapping here also makes the counter independent of when the origin was
	// last reset, which matters because that reset lives in the audio frame
	// flush: with audio disabled, or in a loop that does not render every
	// frame, the offset grows without bound and the masked version became a
	// free-running counter with no relation to the beam.
	t %= TStatesPerLine * LinesPerFrame
	line = t / TStatesPerLine
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
	// entry and restores it at the handler tail ($2040 OUT (C),L), so
	// a write-only select port (returning open bus) would corrupt the
	// guest's NR-select on every interrupt.
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

	// Ports 0x6B / 0x0B: zxnDMA register read-back (status / byte counter /
	// port addresses, selected by the read mask). Decoded on the low 8 bits;
	// the accessing port latches the DMA mode ($6B = zxn, $0B = z80 —
	// zxnext.vhd:1817, on reads as well as writes).
	if lsb := addr & 0xFF; u.nextDMA != nil && (lsb == 0x6B || lsb == 0x0B) {
		u.nextDMA.SetZ80Mode(lsb == 0x0B)
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
	// then a routine ($15F9) tests those bytes against the expected paging
	// state (MF ROM $01F6 `cp $04; jr nz`) to decide whether to continue to
	// the Sinclair 128 menu or abort — so this read must return the real
	// paging register, not open bus. The $Dxxx/$Exxx (dffd/eff7) and border
	// high-nibble cases aren't modelled — ours doesn't track those
	// registers and the launch doesn't read them.
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
	// launch's MF NMI handler reads $123B to snapshot Layer 2 state, so
	// this must return the real latch, not open bus (which would read as
	// bit1=1, "Layer 2 visible", and leave it visibly enabled afterwards).
	if u.nextRegs != nil && addr == 0x123B {
		return u.port123BVal, true
	}

	// Port $303B read: sprite status (bit 0 collision, bit 1
	// max-per-line); reading clears the latched collision flag.
	if u.nextSprite != nil && addr == 0x303B {
		return u.nextSprite.ReadStatus(), true
	}

	// Port $FF3B read: the ULA+ enable read-back. In mode group 00 the port
	// serves palette data instead, which is not modelled here, so the handler
	// declines and the read falls through (zxnext.vhd:4560-4568).
	if u.nextULAPlus != nil && addr == 0xFF3B {
		if v, ok := u.nextULAPlus.ReadFF3B(); ok {
			return v, true
		}
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
		// bit 7 is reserved (reads 1). Spectrum Next's boot.bin (and
		// Sinclair Test ROMs) distinguish "live ULA" from "stuck bus"
		// by reading the reserved bits as 1; a zero there sends them
		// into error-handling paths. The base value is therefore 0xBF
		// (bit 6 = 0 default, bits 5 and 7 = 1) ANDed with the keyboard
		// scan ORed with 0xE0, so the kbd matrix only affects bits 0-4.
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
	// On the Spectrum Next the FPGA ALWAYS decodes $1F as the Kempston joystick
	// (the TBBLUE firmware polls it at boot; the joystick-mode NextReg only
	// selects which physical input drives it), so an idle read returns $00 —
	// not the floating bus. Games rely on this: Sonic reads $1F and complements
	// it (IN A,($1F); XOR $FF; …) to derive an option-menu flag; a floating-bus
	// $FF there inverted the flag and forced a blank-screen path.
	isNext := u.mem != nil && u.mem.GetCurrentModel() == roms.ModelNext
	if (u.KempstonEnabled || isNext) && (addr&0x00E0) == 0x0000 && (addr&0x001F) == 0x001F {
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

	// Per-model line length: the 48K ULA uses 224 T-states/line, the 128K
	// family 228 (video/zxula_timing.vhd c_max_hc 447 vs 455).
	tPerLine := TStatesPerLineFor(model)

	// The first paper fetch of display line 0, measured from the interrupt.
	// This is per-personality and only the 48K's is a whole 64 scanlines —
	// see roms.DisplayStartTState. The same constant anchors pkg/memory's
	// contention window, so the floating bus and contention agree by
	// construction.
	displayStart := roms.DisplayStartTState(model)
	if tstates < displayStart {
		return 0xFF
	}

	line := (tstates - displayStart) / tPerLine
	if line >= 192 { // bottom border
		return 0xFF
	}

	// T-states into this display line, measured from its first paper fetch.
	tInDisplay := tstates - displayStart - line*tPerLine

	// The ULA fetches for 128 T-states of each line; the rest of the line is
	// border and retrace, which return no screen data.
	const horizontalScreen = 128
	if tInDisplay >= horizontalScreen {
		return 0xFF
	}
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
	// Port $FF — the Timex SCLD video-mode register. bits 2:0 select the
	// display mode (110 = 512x192 8x1 hi-res), bits 5:3 the hi-res colour.
	// NextZXOS's 64/85-column text modes (e.g. the .more text viewer) use the
	// hi-res mode. Stored here; rendered by renderTimexHiRes. Falls through so
	// any other $FF semantics are unaffected.
	// Only a machine with an SCLD decodes this. A Sinclair 48K/128K/+3
	// drives $FF as the floating bus and ignores writes to it, so an
	// ordinary OUT (C),r with C = $FF used to switch the emulator into
	// a 640-wide Timex frame on machines that have no such mode.
	if (addr&0xFF) == 0xFF && u.mem != nil && u.mem.GetCurrentModel() == roms.ModelNext {
		u.timexVideoMode = val
		// The Spectrum Next's LoRes layer takes screen-mode bit 0 as half of
		// its display-file select (zxnext.vhd:6796), so a consumer has to hear
		// about the write rather than wait for the next NextReg poke.
		if u.timexModeObserver != nil {
			u.timexModeObserver(val)
		}
	}
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

	// Ports $BF3B / $FF3B: the ULA+ register-select and data ports
	// (zxnext.vhd:2685-2686, full 16-bit decode $BFxx/$FFxx + $3B).
	if u.nextULAPlus != nil {
		switch addr {
		case 0xBF3B:
			u.nextULAPlus.WriteBF3B(val)
			return
		case 0xFF3B:
			// Declined in the palette mode group, which is not modelled: fall
			// through rather than swallow the byte, so the write is visible to
			// whatever handles it later. The read side declines the same group.
			if u.nextULAPlus.WriteFF3B(val) {
				return
			}
		}
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

	// Ports 0x6B / 0x0B: zxnDMA command stream. Decoded on low 8 bits only;
	// the accessing port latches the DMA mode ($6B = zxn, $0B = z80).
	if lsb := addr & 0xFF; u.nextDMA != nil && (lsb == 0x6B || lsb == 0x0B) {
		u.nextDMA.SetZ80Mode(lsb == 0x0B)
		u.nextDMA.WriteCommand(val)
		return
	}

	// Port $303B write: select the active sprite AND pattern-upload cursor
	// (ports.txt 0x303B — sets both quantities from the one value).
	if u.nextSprite != nil && addr == 0x303B {
		u.nextSprite.SelectSlot(val)
		return
	}

	// Port $005B write: stream a byte into the sprite pattern RAM at the
	// current cursor (ports.txt 0x5B). Decoded on the low 8 bits only because
	// OTIR (the canonical pattern-upload loop) varies the high byte via B.
	if u.nextSprite != nil && (addr&0xFF) == 0x5B {
		u.nextSprite.WritePatternByte(val)
		return
	}

	// Port $0057 write: stream a byte into the current sprite's attributes
	// (ports.txt 0x57, "Sprite Attribute Upload"). Each sprite takes 4 or 5
	// bytes, then the current-sprite pointer auto-advances. Decoded on the low
	// 8 bits only because the OTIR upload loop varies the high byte via B — the
	// same convention as the $5B pattern stream above. Nextoid uploads all its
	// sprites (bat, ball, HUD) through this port each frame.
	if u.nextSprite != nil && (addr&0xFF) == 0x57 {
		u.nextSprite.WriteAttr(val)
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
		// Layer-2 write/read paging: route CPU accesses to Layer-2 RAM while
		// enabled (bit 0/2) so a game's Layer-2 screen clear hits Layer-2 RAM,
		// not normal RAM. (zxnext.vhd:3915-3933)
		if u.mem != nil {
			u.mem.SetLayer2MapControl(val)
		}
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

	// Classic-Spectrum SpecDrum ($DF) / Covox ($FB) DAC. When an enabled
	// device claims the port, latch the 8-bit sample with its T-state offset so
	// flushAudioFrame can reconstruct the waveform, and consume the write
	// (claiming $FB is why Covox and the ZX Printer can't both be on at once).
	//
	// This runs ahead of the Next's internal DAC because both decode $DF
	// and $FB. An add-on the user has explicitly enabled wins the port;
	// the internal bank picks it up otherwise.
	if u.speccyDAC != nil && u.speccyDAC.Handles(byte(addr&0xFF)) {
		if u.audio != nil && u.mem.TStates != nil {
			u.speccyDAC.Record(int(*u.mem.TStates-u.frameStartTstate), val)
		}
		return
	}

	// Spectrum Next DAC ports (0x0F / 0x1F / 0x3F / 0x4F / 0x5F / 0xB3 /
	// 0xDF / 0xF1 / 0xF3 / 0xF9 / 0xFB on the low byte). The bank returns true if the
	// port was a DAC channel — when handled, fall through to the
	// rest of the dispatch is unnecessary (DAC ports don't alias
	// classic ULA ports). When the port wasn't a DAC port the bank
	// returns false and we continue with the normal dispatch.
	if u.nextDAC != nil && u.nextDAC.WritePort(addr, val) {
		// Record the timed write so the frame can reconstruct the DAC
		// waveform sample-accurately (event-timed, like the beeper).
		if u.audio != nil && u.mem.TStates != nil {
			u.nextDAC.Record(int(*u.mem.TStates - u.frameStartTstate))
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
			// Record the border change with current scanline for mid-frame rendering.
			// Per-model line length (224 on 48K, 228 on 128K+) — see
			// TStatesPerLineFor and its use in floatingBusByte.
			scanline := 0
			if u.mem.TStates != nil {
				scanline = int(*u.mem.TStates) / TStatesPerLineFor(u.mem.GetCurrentModel())
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
	} else if u.mem.GetCurrentModel() == roms.ModelNext && (addr&0xF0FF) == 0xE0F7 {
		// Port 0xEFF7 (zxnext.vhd:2604): incompletely decoded on address
		// bits 15:12="1110" and low byte $F7 only — bits 11:8 are don't-
		// care, so $E0F7-$EFF7 all alias this port (a classic Pentagon/
		// Scorpion-style port carried through on the Next). Checked
		// before the AY/DFFD patterns below since 0xF0FF doesn't
		// overlap them, but ordering it early keeps the loose-decode
		// port from ever being shadowed by a future broader pattern.
		u.mem.SetEFF7(val)
	} else if u.mem.GetCurrentModel() == roms.ModelNext && (addr&0xF002) == 0xD000 {
		// Port 0xDFFD (Spectrum Next high RAM-bank extension): bits 3:0 are the
		// MSBs of the $C000-slot RAM bank. Must be decoded before the AY
		// register-select port 0xFFFD below, which shares the same
		// (addr&0xC002)==0xC000 pattern — the Next gives 0xDFFD precedence
		// over AY (ports.txt 0xdffd), or RAM banks >= 8 would be unreachable
		// via the classic $C000 slot.
		u.mem.SetDFFD(val)
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
		// ModelNext must be included in this branch: 0x1FFD also matches the
		// loose 0x7FFD pattern below (0x1FFD & 0x8002 == 0), so without the
		// strict decode here a $1FFD write would be misread as a $7FFD
		// paging write and remap the wrong RAM bank into the $C000 slot.
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
// row scratch buffers (compositorScan / compositorComposed /
// compositorRow) are allocated once and reused across frames.
// copperClocksPerHCount mirrors copper.ClocksPerHCount: hcount ticks at
// the 7 MHz pixel clock and the copper is clocked at 28 MHz, so four
// copper clocks pass per column.
const copperClocksPerHCount = 4

func (u *ULA) applyNextCompositor() {
	const w = 256
	const h = 192
	// The Copper is clocked from the 28 MHz video domain, NOT from the CPU
	// clock, so its per-scanline throughput is fixed by the line length:
	// hcount ticks at the 7 MHz pixel clock (two columns per T-state) and
	// the copper gets four clocks per column. The earlier budget of 64 was
	// derived from CPU T-states and starved any list with more than 64
	// instructions on one line.
	// Mirrors copper.ClocksPerScanline, which pkg/ula cannot import (the
	// copper reaches the ULA through the NextCopper interface to keep the
	// dependency one-way). The budget is in copper CLOCKS, the unit Step
	// charges in: a MOVE costs two and everything else one. Budgeting in
	// MOVEs alone left NOOPs and unreleased WAITs free, so a list made of
	// them never drew the budget down and lapped itself many times a line.
	copperClocksPerScanline := TStatesPerLineFor(u.mem.GetCurrentModel()) * 2 * copperClocksPerHCount
	if u.compositorScan == nil {
		u.compositorScan = make([]byte, w*4)
		u.compositorComposed = make([]byte, w*4)
	}
	ulaScan := u.compositorScan
	composed := u.compositorComposed
	// Rewind the journalled CPU writes to their frame-start values; each is
	// re-applied below as the walk reaches the row it was made on. A no-op
	// when nothing was journalled, and when no journal is wired at all.
	journal := u.nextRasterLog
	if journal != nil {
		journal.BeginReplay()
	}
	for y := 0; y < h; y++ {
		// Bring the visual state up to this row before composing it.
		if journal != nil {
			journal.ApplyThrough(y)
		}
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
		rowStart := (BorderTop+y)*u.img.Stride + BorderLeft*4
		copy(ulaScan, u.img.Pix[rowStart:rowStart+w*4])

		// Walk the line in 8-pixel segments, stepping the Copper at each
		// boundary before composing that segment. The Copper's WAIT column
		// field is 6 bits taken as 8-pixel units — the release threshold is
		// hcount >= (X<<3)+12 (device/copper.vhd:94) — so 8 pixels IS the
		// hardware's own horizontal resolution. A MOVE gated by a mid-line
		// WAIT therefore affects exactly the segments after it, which a
		// once-per-line step could not reproduce at all.
		//
		// The whole line still shares one instruction budget, so segmenting
		// cannot let the Copper run faster than the hardware does.
		budget := copperClocksPerScanline
		if u.nextCopper != nil {
			for x := 0; x < w; x += copperSegmentPixels {
				if budget > 0 {
					budget -= u.nextCopper.Step(uint16(y), uint16(x), budget)
				}
				u.nextCompositor.ComposeScanlineRange(y, ulaScan, composed, x, x+copperSegmentPixels)
			}
			// Finish the line off-screen so a WAIT for a column beyond the
			// visible area still releases within its own scanline.
			if budget > 0 {
				u.nextCopper.Step(uint16(y), 511, budget)
			}
		} else {
			u.nextCompositor.ComposeScanline(y, ulaScan, composed)
		}
		copy(u.img.Pix[rowStart:rowStart+w*4], composed)
	}
	// Restore the state the guest actually left, including writes made below
	// the last display row, so the border passes and the next frame both see
	// it. Then drop the frame's entries.
	if journal != nil {
		journal.EndReplay()
	}

	// Border-area tilemap pass. Tilemap content in NextZXOS Browser
	// (40×32 tile grid = 320×256 pixels) extends beyond the classic
	// 256×192 inner screen into the 32-px L/R borders + 24-px T/B
	// borders. The inner pass above already painted tilemap inside
	// the 256×192 box; here we walk the FULL 320×240 image and only
	// touch border pixels.
	if u.nextCompositor.HasActiveTilemap() {
		if u.compositorRow == nil {
			u.compositorRow = make([]byte, TotalWidth*4)
		}
		rowFull := u.compositorRow
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

	// Sprite border pass. Sprites are frame-relative (320x256, paper at 32,32),
	// so this image's row r maps to sprite vcounter r + spriteFrameYBias. The
	// inner paper pass already drew sprites inside the 256x192 box; here we walk
	// the full image and paint sprite pixels only in the border strips — the
	// top/bottom borders (where games park HUD sprites, e.g. Nextoid's
	// SHIPS/SCORE row at frame Y 224-225) and the 32-px L/R borders of screen
	// rows. The sprite engine's over-border clip gates whether they show.
	if u.nextCompositor.HasActiveSprites() {
		// The image (TotalHeight=240, BorderTop) is the centre of the 256-line
		// sprite frame (top border 32): image row r = frame vcounter r + bias.
		const spriteFrameH = 256
		bias := (spriteFrameH - TotalHeight) / 2 // 8 for a 240-line image
		if u.compositorRow == nil {
			u.compositorRow = make([]byte, TotalWidth*4)
		}
		rowFull := u.compositorRow
		for y := 0; y < TotalHeight; y++ {
			imgRowStart := y * u.img.Stride
			copy(rowFull, u.img.Pix[imgRowStart:imgRowStart+TotalWidth*4])
			inBorder := func(x int) bool {
				return x < BorderLeft || x >= BorderLeft+ScreenWidth
			}
			if y < BorderTop || y >= BorderTop+ScreenHeight {
				inBorder = func(int) bool { return true }
			}
			u.nextCompositor.ComposeSpriteBorderRow(y+bias, rowFull, inBorder)
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
	if tp != nil {
		// TZX block 0x2A stops the tape only on a 48K machine, so give the
		// player a live view of the model.
		tp.SetIs48K(func() bool {
			return u.mem != nil && u.mem.GetCurrentModel() == roms.Model48K
		})
	}
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
		if off := int(now - u.frameStartTstate); off >= 0 && off < u.audioFrameTStates() {
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
	u.frameStartBorderColour = 0
	u.Mic = false
	u.TapeIn = false
	u.tapeAudioEvents = u.tapeAudioEvents[:0]
	u.frameStartTapeState = false
	u.Speaker = false
	u.flash = false
	u.flashCount = 0
	u.KempstonState = 0
	// Video-mode latches go with the rest. Leaving timexVideoMode set
	// meant a reboot out of a 64-column NextZXOS screen kept drawing a
	// 640-wide scrambled picture until something wrote $FF again; the
	// same argument covers NextReg $68's ULA-output disable, the ULA
	// scroll offsets ($26/$27) and the $123B read-back shadow, all of
	// which reset to zero on the FPGA.
	u.timexVideoMode = 0
	u.ulaOutputDisabled = false
	u.ulaScrollX, u.ulaScrollY = 0, 0
	u.port123BVal = 0
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
	u.dc.Reset()

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
		u.dc.Reset()
		u.audio.PushBeeperSamples(make([]int16, audio.SamplesPerFrame))
		if u.mem.TStates != nil {
			u.frameStartTstate = *u.mem.TStates
		}
		return
	}
	u.audio.PushStereoSamples(u.mixAudioFrame())
	if u.mem.TStates != nil {
		u.frameStartTstate = *u.mem.TStates
	}
}

// audioFrameTStates is the length of one ULA frame in the units the audio
// events are stamped in — CPU T-states since the frame started.
//
// Two things decide it. The model: 69888 on a 48K, 70908 on the 128K
// family and the Next, 71680 on a Pentagon. And the CPU speed: at 7, 14
// or 28 MHz the Z80 burns SpeedMultiplier times as many T-states inside
// the same 50 Hz frame, and the events carry the CPU clock.
//
// The mixer used to hard-code 69888. On a 128K that made beeper music run
// about 1.4% fast and cut every event in the last ~1020 T of each frame;
// on a Next at 28 MHz it dropped most of the frame outright.
func (u *ULA) audioFrameTStates() int {
	if u.mem == nil {
		return 69888
	}
	n := roms.FrameTStates(u.mem.GetCurrentModel())
	if u.mem.SpeedMultiplier != nil {
		if m := u.mem.SpeedMultiplier(); m > 0 {
			n *= m
		}
	}
	return n
}

// mixAudioFrame builds the interleaved stereo frame for the just-finished
// frame, consuming the recorded events. It mirrors audio_mixer.vhd, which sums
// the beeper, tape, AY and DAC into one pair.
//
// Only the Next's DAC bank is two-sided. The beeper is one bit driving one
// speaker; the tape's EAR line is one bit; SpecDrum and Covox are single-ended
// 8-bit DACs. Those are summed in mono and then widened, which is both cheaper
// and more honest than carrying two identical copies through every stage.
//
// It CONSUMES the frame: every event list it reads is cleared and every carried
// level updated before it returns. That has to be all of them or none — the
// speaker events were once cleared by the caller instead, which meant any
// second caller replayed the same frame for ever with nothing at the call site
// to suggest it.
func (u *ULA) mixAudioFrame() []int16 {
	tstatesPerFrame := u.audioFrameTStates()

	samples, finalState := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState, tstatesPerFrame)
	// Mix the SpecDrum/Covox DAC frame (event-timed, sample-accurate) into the
	// beeper waveform.
	if u.speccyDAC != nil && u.speccyDAC.Enabled() {
		mixInt16(samples, u.speccyDAC.GenerateFrame(audio.SamplesPerFrame, tstatesPerFrame))
	}
	// Tape-loading sound: reconstruct the EAR waveform and mix it in (the
	// audible pilot whistle + data screech). Only while a tape is playing, so
	// there's no DC bias once loading finishes.
	if u.tape != nil && u.tape.IsPlaying() {
		tapeSamples, finalTape := generateSquareWaveFrame(
			u.tapeAudioEvents, u.frameStartTapeState, -tapeAudioAmplitude, tapeAudioAmplitude, tstatesPerFrame)
		mixInt16(samples, tapeSamples)
		u.frameStartTapeState = finalTape
	} else {
		u.frameStartTapeState = false
	}
	u.tapeAudioEvents = u.tapeAudioEvents[:0]
	u.frameStartSpeakerState = finalState
	u.audioEvents = u.audioEvents[:0]

	frame := widenToStereo(samples)

	// Spectrum Next 4-channel DAC: event-timed, and the one source with a real
	// stereo image (soundrive.vhd sums A+B left and C+D right).
	if u.nextDAC != nil {
		mixInt16(frame, u.nextDAC.GenerateFrameStereo(audio.SamplesPerFrame, tstatesPerFrame))
	}

	// AC-couple each channel like the hardware's output capacitor: a held level
	// decays to silence and only edges make sound, so idle/power-on/reset and
	// the gaps between loader blocks no longer step to a full-scale DC rail
	// (the "battery click").
	if u.dcEnabled {
		u.dc.ProcessStereo(frame)
	}
	return frame
}

// widenToStereo turns a mono frame into an interleaved stereo one by putting
// each sample on both channels.
func widenToStereo(mono []int16) []int16 {
	out := make([]int16, len(mono)*2)
	for i, s := range mono {
		out[i*2] = s
		out[i*2+1] = s
	}
	return out
}

// mixInt16 adds src into dst element-wise with int16 saturation. Used to fold
// the DAC frame into the beeper frame without wrap-around pops.
func mixInt16(dst, src []int16) {
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := 0; i < n; i++ {
		dst[i] = audio.SaturatingAdd16(dst[i], int32(src[i]))
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
func generateBeeperFrame(events []audioEvent, initialState bool, tstatesPerFrame int) (samples []int16, finalState bool) {
	return generateSquareWaveFrame(events, initialState, beeperLow, beeperHigh, tstatesPerFrame)
}

// generateSquareWaveFrame is the box-filter square-wave reconstruction shared by
// the beeper and the tape-loading sound: it integrates a 1-bit signal (toggled
// by `events`) into one frame of samples between `low` (state false) and `high`
// (state true). See generateBeeperFrame for why integration (not point-sampling)
// is used.
func generateSquareWaveFrame(events []audioEvent, initialState bool, low, high int16, tstatesPerFrame int) (samples []int16, finalState bool) {
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
