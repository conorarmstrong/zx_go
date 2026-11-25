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

// copperHCountOrigin mirrors copper.HCountOrigin: the hcount at which the
// display generates its first pixel. The Copper's hcount_i is hc_ula, reset
// twelve columns before the 256-pixel active area
// (video/zxula_timing.vhd:423-424; video/zxula.vhd:44-46 states that the
// practical pixel-0 counter "corresponds to ULA count i_hc = 0xC"), so a
// render loop must offset the display x it hands to Step by this. Passing the
// raw x released every WAIT twelve pixels late.
//
// Restated rather than imported, because this package reaches the Copper
// through an interface and does not depend on its implementation. Drift is
// caught by TestCopperConstantsMatchTheDevice rather than by the compiler.
const copperHCountOrigin = 12

// TStatesPerLineFor returns the T-states per scanline for a machine model:
// 224 for the 48K (312 lines * 224 = 69888 T-states/frame) and the Pentagon
// (320 * 224 = 71680), 228 for the 128K family and +2/+2A/+3 (311 * 228 =
// 70908). It is roms.TStatesPerLine, from video/zxula_timing.vhd c_max_hc
// (447, 448 columns, for the 48K and Pentagon; 455 for the 128K family). The
// Pentagon used to get 228 here, against its own 71680-T-state frame.
func TStatesPerLineFor(model roms.SpectrumModel) int {
	return roms.TStatesPerLine(model)
}

// LinesPerFrameFor returns the scanline count of one frame for a machine
// model: 312 for the 48K, 311 for the 128K family and +2/+2A/+3. Matches
// video/zxula_timing.vhd c_max_vc at 50 Hz: 311 for 48K (line 270) and 310
// for 128K (line 204), each being the last line number rather than the count.
//
// Lines are numbered from zero, so the highest the raster reaches is one below
// this.
//
// The Copper needs it twice over: its vertical counter cvc runs the whole
// frame, not just the displayed rows (video/zxula_timing.vhd:455-466), and the
// Copper disassembler needs the same number to tell a WAIT that can never
// release from an ordinary one. Exported so those are one number and not two.
func LinesPerFrameFor(model roms.SpectrumModel) int {
	return roms.LinesPerFrameFor(model, false)
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
	// nextFrame is the full 256-line Next frame renderNext composes into,
	// 320 or 640 pixels wide. Reused across frames.
	nextFrame *image.RGBA
	// nextULAColour, when set, supplies the ULA's colours (the Next ULA
	// palette) in place of the classic fixed palette.
	nextULAColour NextULAColour
	// compositorScan is renderNext's per-row ULA scratch buffer, reused
	// across frames.
	compositorScan []byte
	palette        [16]color.RGBA
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
	// nextMF is the Next Multiface's port decode (see SetNextMultiface).
	nextMF NextMultiface
	// in123BWrite marks port $123B's own update of NR$69, which is not an
	// NR$69 write for port_ff_reg (see NextRegWritten).
	in123BWrite bool
	// syncAt and syncLen describe a frame whose end a frame sync moved: from
	// syncAt video T-states the frame is the new timing's run-up to an
	// interrupt syncLen T-states after the frame's start. 0 when none.
	syncAt, syncLen int

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

	// Mid-frame border tracking: records (T-state, colour) pairs for each
	// border change, so Render can paint each one from the beam position of
	// the write. See paintBorder.
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

	// sixtyHz selects the Next's 60 Hz video timing (NextReg 0x05 bit 2).
	// It changes the frame length, scanline count, display origin and
	// contention origin together; see roms.FrameTStatesFor and
	// DisplayStartTStateFor. The pixel clock and line length are unchanged.
	sixtyHz bool
	// modelSet is true for a ULA built by New. Test constructors commonly
	// use a zero-valued memory.Memory (model 48K) but have always meant
	// the 128K-family 228/311 geometry; the flag preserves that contract
	// without guessing from a zero model.
	modelSet bool

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

	// nextCTC receives the $183B..$1F3B CTC port group. Wired only for
	// ModelNext.
	nextCTC NextCTC
	// nextUART receives the UART register ports. Wired only for ModelNext.
	nextUART NextUART

	// nextSprite receives port $303B traffic: a write selects the
	// active sprite, a read returns the sprite status (collision /
	// max-per-line, clear-on-read). Wired only for ModelNext.
	nextSprite NextSpritePort

	// nextRasterLog, when wired, replays this frame's CPU writes to the
	// Next's visual state at the row each was made on (see NextRasterLog).
	nextRasterLog NextRasterLog

	// nextCopper is clocked column by column across every scanline of the
	// frame during the post-render compositor pass. nil on non-Next models.
	nextCopper NextCopper
	// copperDebt is the copper clocks a MOVE spent past the end of the column
	// it began in, carried forward to be taken off the next one. The copper is
	// clocked from the free-running i_CLK_28 (zxnext.vhd:43,3944) and nothing
	// restarts it at a column, line or frame edge, so the debt lives here
	// rather than in renderNext: a MOVE begun on the frame's last
	// clock is paid for out of the next frame's first column.
	copperDebt int
	// audioHeld keeps a frame's samples from the host while the frame is
	// still synthesised, so every generator advances as it would. Replay
	// re-runs frames the user has already heard.
	audioHeld bool
	// nextLayerCacheOff makes every Copper write re-render the row's layers,
	// as before the cache existed. Tests compare the two paths with it.
	nextLayerCacheOff bool

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
	// ComposeScanlineRange composes pixels [x0, x1) of a paper row.
	ComposeScanlineRange(y int, ulaRGBA []byte, dst []byte, x0, x1 int)
	// ComposeFrameRowRange composes output pixels [x0, x1) of frame row fy
	// (0..255 of the 320x256 frame, paper at rows 32..223). ula holds the
	// ULA layer for the whole row, border included, and dst receives the
	// result; both are FullWidth*scale pixels of RGBA. scale is 1 for a
	// 320-pixel row and 2 for a 640-pixel one. Every layer is sampled at its
	// own resolution and the NR$15 order is applied per pixel.
	ComposeFrameRowRange(fy int, ula, dst []byte, scale, x0, x1 int)
	// HasActiveSprites reports whether the sprite layer is enabled.
	HasActiveSprites() bool
	// HiResLayer2Active reports whether Layer 2 is enabled at 320x256 or
	// 640x256.
	HiResLayer2Active() bool
	// WideScale is 2 when a layer needs a 640-pixel row (the 80-column
	// tilemap or the 640x256 Layer 2) and 1 otherwise.
	WideScale() int
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

// NextCTC is the contract for the Spectrum Next's four-channel CTC on
// ports $183B..$1F3B. pkg/next/ctc.Bank satisfies it. Both calls report
// whether the address belongs to the CTC: $1C3B..$1F3B decode as the
// CTC but select no channel, and must not fall through to the ordinary
// port dispatch.
type NextCTC interface {
	ReadPort(addr uint16) (byte, bool)
	WritePort(addr uint16, val byte) bool
}

// NextUART is the contract for the Spectrum Next's UART register block on
// ports $133B, $143B, $153B and $163B (zxnext.vhd:2639). Each method reports
// whether it claimed the port, so a non-UART address falls through.
type NextUART interface {
	ReadPort(addr uint16) (byte, bool)
	WritePort(addr uint16, val byte) bool
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
// satisfies it. The render loop calls Step once per raster column of
// every active scanline, so a MOVE that affects palette / Layer 2 state
// takes effect for the rest of the row and not before it.
//
// Wrote reports whether that one Step raised a MOVE's write pulse, which is
// how the loop learns which pixel to divide the row's composition at.
type NextCopper interface {
	Step(scanline uint16, hcount uint16, maxClocks int) int
	Wrote() bool
	// Idle reports that clocking the Copper cannot change anything, so a row's
	// worth of columns can be skipped rather than stepped one at a time. Almost
	// no program enables the Copper, and stepping it anyway costs a couple of
	// percent of the frame budget in call overhead alone.
	Idle() bool
}

// nextCopperRegs is the optional part of NextCopper that names the registers
// the last Step wrote and predicts whether the next Step will write.
// pkg/next/copper.Copper satisfies it.
type nextCopperRegs interface {
	WroteRegs() []byte
	WouldWrite(scanline uint16, hcount uint16, maxClocks int) bool
}

// nextLayerCache is the optional part of NextCompositor that re-composes a
// row's tail without rendering its layers again, for Copper writes that
// cannot change a layer's render. pkg/next/compositor.Compositor satisfies
// it; see compositor.LayerRenderReads for the register set.
type nextLayerCache interface {
	ComposeFrameRowTail(fy int, ula, dst []byte, scale, x0, x1 int)
	InvalidateLayerRenders()
	LayerRenderReads(reg byte) bool
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
	// SuspendRecording keeps writes made on the machine's own behalf out of the
	// journal, for the stretch of a frame that is outside the replay window.
	// The Copper runs below the last display row, and its writes there are not
	// the guest's to replay.
	SuspendRecording() (resume func())
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

// Set60Hz selects 60 Hz video timing for the Spectrum Next (NextReg
// 0x05 bit 2). The line length is unchanged; the frame shortens from
// 311/312 lines to 264 and the display/contention origin moves up.
// It is a no-op for models without a 60 Hz timing mode.
func (u *ULA) Set60Hz(on bool) { u.sixtyHz = on }

// SixtyHz reports the selected video timing.
func (u *ULA) SixtyHz() bool { return u.sixtyHz }

// FrameTStates returns the live frame length in 3.5 MHz T-states for the
// current model and video mode.
func (u *ULA) FrameTStates() int {
	if u.mem == nil {
		return 69888
	}
	return roms.FrameTStatesFor(u.mem.TimingModel(), u.sixtyHz)
}

// tStatesPerLine is the live scanline length. The zero-value ULA used by
// unit tests has always meant the 128K 228-T line; keep that contract.
func (u *ULA) tStatesPerLine() int {
	if u.mem == nil || !u.modelSet {
		return TStatesPerLine
	}
	return TStatesPerLineFor(u.mem.TimingModel())
}

// linesPerFrame is the live scanline count for the current model and mode.
func (u *ULA) linesPerFrame() int {
	if u.mem == nil || !u.modelSet {
		return LinesPerFrame
	}
	return roms.LinesPerFrameFor(u.mem.TimingModel(), u.sixtyHz)
}

// displayStartTState is the live frame-relative T-state of the first paper
// fetch, for the current model and video mode.
func (u *ULA) displayStartTState() int {
	if u.mem == nil || !u.modelSet {
		return roms.DisplayStartTState(roms.Model128K)
	}
	return roms.DisplayStartTStateFor(u.mem.TimingModel(), u.sixtyHz)
}

// SetNextCompositor installs the Spectrum Next render stack's
// scanline compositor. Once installed, Render overlays the
// composited output on top of the 256x192 active display region.
// Passing nil restores the plain-ULA render.
func (u *ULA) SetNextCompositor(c NextCompositor) { u.nextCompositor = c }

// NextCompositor returns the compositor SetNextCompositor installed.
func (u *ULA) NextCompositor() NextCompositor { return u.nextCompositor }

// Palette returns the ULA's 16-colour palette. The Next compositor uses it
// to resolve the ULA transparency colour: the classic ULA renders via this
// palette, so the global transparency NR$14 (when < 16) corresponds to
// u.palette[NR$14], which is the colour a transparent ULA pixel carries.
func (u *ULA) Palette() [16]color.RGBA { return u.palette }

// SetNextDMA installs the Spectrum Next zxnDMA controller. Port
// 0x6B writes are forwarded as command bytes. Passing nil unhooks.
func (u *ULA) SetNextDMA(d NextDMA) { u.nextDMA = d }

// SetNextCTC installs the Spectrum Next four-channel CTC. Ports
// $183B..$1F3B route to it; nil unhooks.
func (u *ULA) SetNextCTC(c NextCTC) { u.nextCTC = c }

// SetNextUART wires the UART register block. Passing nil unhooks it.
func (u *ULA) SetNextUART(c NextUART) { u.nextUART = c }

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

// HoldAudio keeps rendered frames' audio from the host (on) or sends it (off,
// the default). The frame's audio is synthesised either way, so the machine
// advances identically; only the push to the audio device is skipped.
func (u *ULA) HoldAudio(on bool) { u.audioHeld = on }

// SetNextLayerCache turns the compositor's per-row layer cache on (the
// default) or off. Off, every Copper write re-renders the row's layers.
func (u *ULA) SetNextLayerCache(on bool) { u.nextLayerCacheOff = !on }

// SetNextRasterLog installs the mid-frame raster journal. Passing nil (the
// default, and every classic model) leaves the compositor pass exactly as it
// was. See NextRasterLog.
func (u *ULA) SetNextRasterLog(l NextRasterLog) { u.nextRasterLog = l }

// FrameID identifies the frame currently being executed: the CPU's frame
// epoch, paired with the clock in video T-states divided by the model's frame
// length. The epoch is needed because ExecuteFrame rewinds the clock at every
// frame end; the video conversion because at a turbo speed the CPU clock runs
// SpeedMultiplier T-states per video T-state.
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
	frame := u.FrameTStates()
	if frame <= 0 {
		return 0
	}
	id := uint64(u.videoTStates(int(*u.mem.TStates))) / uint64(frame)
	if u.mem.FrameEpoch != nil {
		// ExecuteFrame rewinds the clock at every frame end, so the clock
		// alone repeats; the epoch counts the rewinds.
		id |= *u.mem.FrameEpoch << 32
	}
	return id
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
	t := u.videoTStates(int(*u.mem.TStates - u.frameStartTstate))
	start := u.displayStartTState()
	if t < start {
		return -1
	}
	row := (t - start) / u.tStatesPerLine()
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
	tstate int // raster T-state of the write, from the frame interrupt
	colour byte
}

// audioEvent records a single speaker-bit toggle within a frame, with
// the T-state offset (0..tstatesPerFrame) at which it happened.
type audioEvent struct {
	tstateOffset int
	state        bool
}

// rasterTState is the frame T-state of the moment, in video T-states from the
// frame interrupt.
func (u *ULA) rasterTState() int {
	if u.mem.TStates == nil {
		return 0
	}
	return u.videoTStates(int(*u.mem.TStates))
}

// videoTStates converts a count of CPU T-states to video T-states. The raster
// runs on the video clock; at a turbo speed the CPU counter runs
// SpeedMultiplier T-states per video T-state (as audioFrameTStates allows
// for).
func (u *ULA) videoTStates(t int) int {
	if u.mem.SpeedMultiplier != nil {
		if m := u.mem.SpeedMultiplier(); m > 1 {
			t /= m
		}
	}
	return t
}

// paintBorder paints the border area of u.img from the frame's recorded
// border changes, each from the beam position of its write.
//
// The mapping follows Fuse (display_dirty.c display_get_beam_position,
// machine.c line_times). Image row y is drawn from T-state
//
//	displayStart - BorderTop lines - BorderLeft/2 + y*line
//
// at 2 pixels per T-state, and a write paints from the unit that contains it
// onward. The Sinclair ULAs latch the border colour once per 8-pixel column,
// 4 T: zxula.vhd:423-429 reloads attr_reg with the border colour only on
// sload. The Pentagon reloads it on every pixel (zxula.vhd:443-447), so there
// the unit is one T-state, 2 pixels.
func (u *ULA) paintBorder(changes []borderChange, colour byte) {
	model := u.mem.TimingModel()
	line := u.tStatesPerLine()
	step := 4
	if model == roms.ModelPentagon {
		step = 1
	}
	unitPx := step * 2
	first := u.displayStartTState() - BorderTop*line - BorderLeft/2

	next := 0
	for y := 0; y < TotalHeight; y++ {
		inPaperRows := y >= BorderTop && y < BorderTop+ScreenHeight
		rowT := first + y*line
		for x := 0; x < TotalWidth; x += unitPx {
			// A write anywhere inside this unit shows from its start.
			end := rowT + (x/unitPx+1)*step
			for next < len(changes) && changes[next].tstate < end {
				colour = changes[next].colour
				next++
			}
			if inPaperRows && x >= BorderLeft && x < BorderLeft+ScreenWidth {
				continue
			}
			c := u.borderRGBA(colour)
			for px := x; px < x+unitPx; px++ {
				u.img.SetRGBA(px, y, c)
			}
		}
	}
}

// New creates a new ULA instance.
func New(mem *memory.Memory, kbd *keyboard.Keyboard) *ULA {
	u := &ULA{
		mem:      mem,
		kbd:      kbd,
		modelSet: true,
		img:      image.NewRGBA(image.Rect(0, 0, TotalWidth, TotalHeight)),
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

	// This frame's border changes and the colour before the first of them.
	// The slice keeps its backing array: nothing writes the port until the
	// render returns.
	borderChanges, borderStart := u.borderChanges, u.frameStartBorderColour
	if len(borderChanges) == 0 {
		// No port writes: BorderColour may still have been set directly (a
		// snapshot load does), so it, not the baseline, is the colour.
		borderStart = u.BorderColour
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
			return u.renderNext()
		}
		return u.img
	}

	u.paintBorder(borderChanges, borderStart)

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

			if u.nextULAColour != nil {
				// The Next ULA palette, through the FPGA's index (flash
				// included).
				ink = u.nextULAColour.Colour(attr, true, u.flash, false)
				paper = u.nextULAColour.Colour(attr, false, u.flash, false)
			} else if u.flash && (attr&0x80) != 0 {
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
		return u.renderNext()
	}

	// Timex 512x192 8x1 hi-res (port $FF mode 110): the NextZXOS 64/85-column
	// text modes (e.g. the .more text viewer) use it. Rendered as a 640-wide
	// frame, like the other wide modes.
	if u.timexHiResActive() {
		return u.renderTimexHiRes()
	}

	return u.img
}

// nextFrameRowBias maps a row of the 240-line image to a row of the Next's
// 256-line frame: the image starts this many lines into it. The tilemap, the
// hi-res Layer 2 and the sprites are all addressed by the frame row (the wide
// vertical counter, zxula_timing.vhd:477-499).
const nextFrameRowBias = (256 - TotalHeight) / 2

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

// NextULAColour is the Spectrum Next ULA's colour path: the palette index the
// FPGA forms from the attribute, the pixel bit and the mode, looked up in the
// Next ULA palette. pkg/next.ULAColour satisfies it.
type NextULAColour interface {
	Colour(attr byte, set, flash, border bool) color.RGBA
}

// SetNextULAColour attaches the Next ULA colour path. nil restores the classic
// fixed palette.
func (u *ULA) SetNextULAColour(c NextULAColour) { u.nextULAColour = c }

// borderRGBA is the colour of border colour c: the Next colour path when one
// is wired, else the classic palette.
func (u *ULA) borderRGBA(c byte) color.RGBA {
	if u.nextULAColour != nil {
		c &= 0x07 // the ULA border is three bits (zxula.vhd:418)
		return u.nextULAColour.Colour(c<<3|c, false, false, true)
	}
	return u.palette[c]
}

// SetNextULAPlus attaches the ULA+ port handler. nil unhooks.
func (u *ULA) SetNextULAPlus(p NextULAPlus) { u.nextULAPlus = p }

// NextMultiface is the Next Multiface's I/O side: Port applies an IN (write
// false) or OUT and reports whether the Multiface answers the IN with a
// paging register (mf_port_en_o). pkg/next.Multiface satisfies it.
type NextMultiface interface {
	Port(addr uint16, write bool) bool
}

// SetNextMultiface attaches the Multiface's port decode. nil unhooks.
func (u *ULA) SetNextMultiface(m NextMultiface) { u.nextMF = m }

// NextRegWritten applies the NextReg writes that also land in port_ff_reg,
// the Timex register (zxnext.vhd:3611-3621): NR$69 bits 5:0 into bits 5:0,
// NR$22 bit 2 into bit 6, and NR$C4 bit 0, inverted, into bit 6. Port $123B's
// own update of NR$69 is not an NR$69 write on the FPGA and does not call this.
func (u *ULA) NextRegWritten(reg, val byte) {
	if u.in123BWrite {
		return
	}
	ff := u.timexVideoMode
	switch reg {
	case 0x69:
		ff = ff&0xC0 | val&0x3F
	case 0x22:
		ff = ff&^0x40 | (val&0x04)<<4
	case 0xC4:
		ff = ff&^0x40 | (^val&0x01)<<6
	default:
		return
	}
	if ff != u.timexVideoMode {
		u.timexVideoMode = ff
		if u.timexModeObserver != nil {
			u.timexModeObserver(ff)
		}
	}
}

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

// ActiveVideoLine returns the current raster line counted from the frame
// origin (T-states since frame start / T-states-per-line), wrapped at the
// frame. It is NOT what the Next's NR$1E/$1F read: those read the copper's
// cvc, which counts from the first paper line; see next.ActiveVideoLine, which
// is computed from VideoTStatesInFrame.
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
	t := u.VideoTStatesInFrame()
	lineLen := u.tStatesPerLine()
	line = t / lineLen
	hpos = (t % lineLen) / 4
	return line, hpos
}

// VideoTStatesInFrame is the beam position as video T-states (the 3.5 MHz
// clock the raster runs on, whatever the CPU speed) since the frame origin,
// wrapped at the frame. 0 when no T-state source is wired.
func (u *ULA) VideoTStatesInFrame() int {
	if u.mem == nil || u.mem.TStates == nil {
		return 0
	}
	t := int(*u.mem.TStates - u.frameStartTstate)
	if t < 0 {
		t = 0
	}
	t = u.videoTStates(t)
	frame := u.tStatesPerLine() * u.linesPerFrame()
	if u.syncLen > 0 && t >= u.syncAt {
		if t < u.syncLen {
			// Past the frame sync: the timing changed there, and the frame
			// ends at the new timing's interrupt, so the beam is that far
			// before it.
			return frame - (u.syncLen - t)
		}
		// Later frames (the single-step driver does not rebase) sit on a
		// grid anchored at the moved end.
		return (t - u.syncLen) % frame
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
	return t % frame
}

// SetFrameSyncTransition records that this frame's sync, at video T-state at,
// changed the timing and moved the frame's end to length. It holds until
// ClearFrameSyncTransition, which the frame loop calls at the next frame.
func (u *ULA) SetFrameSyncTransition(at, length int) { u.syncAt, u.syncLen = at, length }

// ClearFrameSyncTransition ends the transition SetFrameSyncTransition set.
func (u *ULA) ClearFrameSyncTransition() { u.syncAt, u.syncLen = 0, 0 }

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

	// Ports $183B..$1F3B: the Next CTC's four channels. The decode is
	// owned by the device even when it selects no channel ($1C3B..$1F3B),
	// so a handled=false from the bank would be a bug, not a fall-through.
	if u.nextCTC != nil {
		if v, ok := u.nextCTC.ReadPort(addr); ok {
			return v, true
		}
	}

	// Ports $133B, $143B, $153B, $163B: the UART register block.
	if u.nextUART != nil {
		if v, ok := u.nextUART.ReadPort(addr); ok {
			return v, true
		}
	}

	// Multiface port read. Every IN reaches the Multiface, whose enable and
	// disable ports page it in and out (multiface.vhd:171-184). An enable
	// read it answers (mf_port_en_o) returns mf_port_dat (zxnext.vhd:4309-
	// 4320): in MF +3 mode a paging register chosen by A15:12, else 7FFD bit 3
	// over ones. NextZXOS's 128K-BASIC launch reads $7F3F/$1F3F this way to
	// snapshot the paging into MF RAM.
	if u.nextMF != nil && u.mem != nil && u.nextMF.Port(addr, false) {
		p7ffd, p1ffd, _ := u.mem.GetPortState()
		if u.nextRegs != nil && u.nextRegs.ReadReg(0x0A)>>6 != 0 {
			return (p7ffd&0x08)<<4 | 0x7F, true // port_7ffd_reg(3) & "1111111"
		}
		switch addr >> 12 {
		case 0x1:
			return p1ffd & 0x0F, true
		case 0x7:
			return p7ffd, true
		case 0xD:
			dffd := u.mem.DFFDValue()
			return dffd&0x40 | dffd&0x1F, true
		case 0xE:
			return u.mem.EFF7Value(), true
		default:
			return u.BorderColour & 0x07, true
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
	if u.mem != nil && u.mem.GetCurrentModel() == roms.ModelNext {
		if v, ok := u.nextFloatingBus(addr); ok {
			return v, true
		}
	}
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
	return u.ulaFloatingBus()
}

// nextFloatingBus answers the Next's two floating-bus ports, which follow its
// NR$03 machine timing. Port $FF reads the Timex register when NR$08 bit 2 is
// set, else the ULA's floating bus on 48K and 128K timing and $FF on the
// others (zxnext.vhd:2813, :4513). On +3 timing, A15:12 = 0 with A1:0 = 01
// is the +3 floating bus, $FF once paging is locked (:2578, :2589, :4517).
// Both sit after every other port handler, so a port another device answers
// keeps its answer.
func (u *ULA) nextFloatingBus(addr uint16) (byte, bool) {
	tm := u.mem.TimingModel()
	switch {
	case addr&0xFF == 0xFF:
		if u.nextRegs != nil && u.nextRegs.ReadReg(0x08)&0x04 != 0 {
			return u.timexVideoMode, true
		}
		if tm == roms.Model48K || tm == roms.Model128K {
			return u.ulaFloatingBus(), true
		}
		return 0xFF, true
	case addr&0xF003 == 0x0001 && tm == roms.ModelPlus3: // port_fd is A1:0 = 01 (zxnext.vhd:2578)
		if !u.mem.PagingEnabled {
			return 0xFF, true
		}
		// While fetching, the fetched byte with bit 0 forced on +3 timing;
		// otherwise the last CPU access to contended memory (zxula.vhd:573).
		if v, ok := u.ulaFetch(); ok {
			return v | 0x01, true
		}
		return u.mem.P3FloatingBus(), true
	}
	return 0, false
}

// ulaFloatingBus is the byte the ULA is fetching at this moment, from the
// timing in force, or $FF between fetches.
func (u *ULA) ulaFloatingBus() byte {
	v, _ := u.ulaFetch()
	return v
}

// ulaFetch is the byte the ULA is fetching at this moment, with true, or
// $FF and false between fetches.
func (u *ULA) ulaFetch() (byte, bool) {
	if u.mem == nil || u.mem.TStates == nil {
		return 0xFF, false
	}

	// Compute T-state offset within the current frame.
	tstates := int(*u.mem.TStates - u.frameStartTstate)

	// Per-model line length: the 48K ULA uses 224 T-states/line, the 128K
	// family 228 (video/zxula_timing.vhd c_max_hc 447 vs 455).
	tPerLine := u.tStatesPerLine()

	// The first paper fetch of display line 0, measured from the interrupt.
	// This is per-personality and only the 48K's is a whole 64 scanlines —
	// see roms.DisplayStartTState. The same constant anchors pkg/memory's
	// contention window, so the floating bus and contention agree by
	// construction.
	displayStart := u.displayStartTState()
	if tstates < displayStart {
		return 0xFF, false
	}

	line := (tstates - displayStart) / tPerLine
	if line >= 192 { // bottom border
		return 0xFF, false
	}

	// T-states into this display line, measured from its first paper fetch.
	tInDisplay := tstates - displayStart - line*tPerLine

	// The ULA fetches for 128 T-states of each line; the rest of the line is
	// border and retrace, which return no screen data.
	const horizontalScreen = 128
	if tInDisplay >= horizontalScreen {
		return 0xFF, false
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
		return 0xFF, false
	}

	switch tInDisplay % 8 {
	case 2:
		return page[screenAddrForRowCol(line, column)], true
	case 3:
		return page[0x1800+(line/8)*32+column], true
	case 4:
		return page[screenAddrForRowCol(line, column+1)], true
	case 5:
		return page[0x1800+(line/8)*32+column+1], true
	}
	return 0xFF, false
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

	// The Multiface's enable and disable ports (multiface.vhd:137-163). Its
	// strobes are decoded independently of every other device
	// (zxnext.vhd:2730-2733), and $3F and $1F are also DAC ports, so it sees
	// the OUT before the dispatch below, which stops at the first claimant.
	// An OUT carries no data into the Multiface, so nothing is consumed.
	if u.nextMF != nil {
		u.nextMF.Port(addr, true)
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

	// Ports $183B..$1F3B: the Next CTC.
	if u.nextCTC != nil && u.nextCTC.WritePort(addr, val) {
		return
	}

	// Ports $133B, $143B, $153B, $163B: the UART register block.
	if u.nextUART != nil && u.nextUART.WritePort(addr, val) {
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
		u.in123BWrite = true
		u.nextRegs.WriteReg(0x69, nr69)
		u.in123BWrite = false
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
			// Record the border change with the raster T-state of the write,
			// for Render to paint from its beam position.
			at := u.rasterTState()
			scanline := at / u.tStatesPerLine()
			u.borderChanges = append(u.borderChanges, borderChange{tstate: at, colour: newBorder})
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

// copperClocksPerHCount mirrors copper.ClocksPerHCount: hcount ticks at
// the 7 MHz pixel clock and the copper is clocked at 28 MHz, so four
// copper clocks pass per column.
const copperClocksPerHCount = 4

// stepCopperColumn clocks the Copper through one raster column of line y,
// paying any straddle debt out of that column, and returns the debt to carry
// into the next one.
//
// A column holds exactly four copper clocks (28 MHz copper, 7 MHz hcount:
// zxnext.vhd:43,46,3944) and unspent ones are discarded rather than banked,
// because the hardware clocks the Copper every tick whether it has anything to
// do or not. A MOVE costs two (device/copper.vhd:87,105), so one begun on a
// column's last clock finishes in the next column and leaves it three.
//
// "The next column" means the next one anywhere: the debt is held on the ULA
// (u.copperDebt) rather than in the frame loop, because i_CLK_28 is
// free-running and the only thing device/copper.vhd does at a frame edge is
// reset the list address in mode 11 (device/copper.vhd:80-83), which moves the
// program counter and not the clock. So the debt crosses a line boundary and a
// frame boundary alike.
func (u *ULA) stepCopperColumn(y, hc, debt int) int {
	clocks := copperClocksPerHCount - debt
	debt = u.nextCopper.Step(uint16(y), uint16(hc), clocks) - clocks
	if debt < 0 {
		return 0
	}
	return debt
}

// renderNext composes the Next's layers over the ULA picture and returns the
// frame. Every frame row is composed whole, border and paper together, through
// the compositor's single pixel pipeline, at 320 pixels or 640 when a layer
// needs it. Rows are composed in the state they were drawn in: the journal is
// rewound to the frame start, the top border takes the writes made before the
// display, each paper row its own, and the bottom border the state at the end
// of the display. The Copper is stepped across each paper row and the row is
// re-composed from every pixel a Copper write lands in.
//
// The frame is built at its full 256 lines. It is returned whole when the
// sprites or a 320x256 / 640x256 Layer 2 are active, which can draw in the
// 8-line strips the classic 240-line image crops, and as the centre 240 lines
// otherwise.
func (u *ULA) renderNext() *image.RGBA {
	const fullH = 256
	timex := u.timexHiResActive()
	scale := u.nextCompositor.WideScale()
	if timex {
		// The Timex hi-res paper is drawn at 512 over a doubled frame; the
		// layers are composed at 320 underneath it.
		scale = 1
	}
	outW := TotalWidth * scale
	if u.nextFrame == nil || u.nextFrame.Rect.Dx() != outW {
		u.nextFrame = image.NewRGBA(image.Rect(0, 0, outW, fullH))
	}
	if len(u.compositorScan) != outW*4 {
		u.compositorScan = make([]byte, outW*4)
	}
	ulaRow := u.compositorScan
	frame := u.nextFrame

	// The ULA layer of frame row fy: the classic image covers rows
	// nextFrameRowBias..nextFrameRowBias+TotalHeight-1; the strips above and
	// below it are border. At scale 2 each ULA pixel is doubled, with the
	// NR$68 bit 2 half-pixel scroll applied (zxula.vhd:353).
	fine := 0
	if u.ulaFineScrollX && scale == 2 {
		fine = 1
	}
	bc := u.borderRGBA(u.BorderColour & 0x0F)
	loadULA := func(fy int) {
		r := fy - nextFrameRowBias
		if r < 0 || r >= TotalHeight {
			for x := 0; x < outW; x++ {
				o := x * 4
				ulaRow[o], ulaRow[o+1], ulaRow[o+2], ulaRow[o+3] = bc.R, bc.G, bc.B, 0xFF
			}
			return
		}
		src := u.img.Pix[r*u.img.Stride : r*u.img.Stride+TotalWidth*4]
		if scale == 1 {
			copy(ulaRow, src)
			return
		}
		for x := 0; x < TotalWidth; x++ {
			s := x * 4
			d := x*8 - fine*4
			if d < 0 {
				copy(ulaRow[0:4], src[s:s+4])
				continue
			}
			copy(ulaRow[d:d+4], src[s:s+4])
			if d+7 < len(ulaRow) {
				copy(ulaRow[d+4:d+8], src[s:s+4])
			}
		}
	}
	composeRow := func(fy int) {
		loadULA(fy)
		dst := frame.Pix[fy*frame.Stride : fy*frame.Stride+outW*4]
		u.nextCompositor.ComposeFrameRowRange(fy, ulaRow, dst, scale, 0, outW)
	}

	// The Copper is clocked from the 28 MHz video domain, not the CPU clock.
	// It is paid per column in copper clocks, where a MOVE costs two and
	// everything else one. columnsPerLine is the hcount column count, two
	// per T-state (video/zxula_timing.vhd:160,196).
	// On the Next the line follows NR$03's machine timing.
	columnsPerLine := u.tStatesPerLine() * 2

	journal := u.nextRasterLog
	if journal != nil {
		journal.BeginReplay()
		journal.ApplyThrough(-1)
	}
	paperTop := nextFrameRowBias + BorderTop // frame row of paper row 0
	// The layer cache needs both halves: a compositor that can reuse a row's
	// renders and a Copper that names the registers it wrote.
	cache, _ := u.nextCompositor.(nextLayerCache)
	regs, _ := u.nextCopper.(nextCopperRegs)
	if u.nextLayerCacheOff {
		cache = nil
	}
	for fy := 0; fy < paperTop; fy++ {
		composeRow(fy)
	}
	for y := 0; y < ScreenHeight; y++ {
		if journal != nil {
			journal.ApplyThrough(y)
		}
		fy := paperTop + y
		loadULA(fy)
		dst := frame.Pix[fy*frame.Stride : fy*frame.Stride+outW*4]
		copperOn := u.nextCopper != nil && !u.nextCopper.Idle()
		if copperOn && cache != nil && regs != nil {
			// Paint the row in segments, each in the state it was drawn in.
			// Before each column the Copper is asked whether it is about to
			// write; if so, the pixels before the column it writes in are
			// painted first, while the state the write changes is still in
			// place. Each pixel is painted once, so a row costs its width plus
			// a little per write, not its width per write. The layers are
			// rendered once per row and again only after a write that names a
			// register a render reads. hcount is hc_ula, whose zero point is
			// twelve columns before displayed paper pixel 0. A write past the
			// right border stands for the next row but shows on none of this
			// one. A layer state that owns no pixel of the row is never
			// rendered, so the sprite engine's sticky collision and overtime
			// flags record only sprite state that reached the screen.
			cache.InvalidateLayerRenders()
			painted := 0
			paintTo := func(x int) {
				if x > outW {
					x = outW
				}
				if x > painted {
					cache.ComposeFrameRowTail(fy, ulaRow, dst, scale, painted, x)
					painted = x
				}
			}
			for hc := 0; hc < columnsPerLine; hc++ {
				if regs.WouldWrite(uint16(y), uint16(hc), copperClocksPerHCount-u.copperDebt) {
					fx := BorderLeft + hc - copperHCountOrigin
					if fx < 0 {
						fx = 0
					}
					paintTo(fx * scale)
				}
				u.copperDebt = u.stepCopperColumn(y, hc, u.copperDebt)
				if !u.nextCopper.Wrote() {
					continue
				}
				for _, r := range regs.WroteRegs() {
					if cache.LayerRenderReads(r) {
						cache.InvalidateLayerRenders()
						break
					}
				}
			}
			paintTo(outW)
			continue
		}
		u.nextCompositor.ComposeFrameRowRange(fy, ulaRow, dst, scale, 0, outW)
		// Without the layer cache: walk the line column by column, stepping
		// the Copper at each hcount, and re-compose the row from the pixel a
		// write lands in to the end of the row; the pixels before it keep the
		// state they were generated under.
		if copperOn {
			for hc := 0; hc < columnsPerLine; hc++ {
				u.copperDebt = u.stepCopperColumn(y, hc, u.copperDebt)
				if !u.nextCopper.Wrote() {
					continue
				}
				fx := BorderLeft + hc - copperHCountOrigin
				if fx >= TotalWidth {
					continue
				}
				if fx < 0 {
					fx = 0
				}
				u.nextCompositor.ComposeFrameRowRange(fy, ulaRow, dst, scale, fx*scale, outW)
			}
		}
	}
	for fy := paperTop + ScreenHeight; fy < fullH; fy++ {
		composeRow(fy)
	}
	// Restore the state the guest actually left, including writes made below
	// the last display row, so the next frame sees it. Then drop the entries.
	if journal != nil {
		journal.EndReplay()
	}

	// The rest of the frame is copper time too. cvc is loaded at the first
	// active line and then counts EVERY line to c_max_vc
	// (video/zxula_timing.vhd:455-466), so display row y is cvc y and the lines
	// below the last displayed one are as real to the Copper as the displayed
	// ones. Nothing gates it on active video on the way: the entity has no
	// display or blanking input at all (device/copper.vhd:28-42), its process is
	// a plain rising_edge(clock_i) with no clock enable (device/copper.vhd:53-57),
	// clock_i is the free-running i_CLK_28 (zxnext.vhd:43,3944), and the only
	// enable is copper_en_i = nr_62_copper_mode, written solely from NextReg
	// $62 (zxnext.vhd:3947,5430). Clocking only the 192 displayed rows starved a
	// list of well over a third of its per-frame instructions and left a WAIT
	// for a border or blanking line parked for ever.
	//
	// Nothing composites here, so this only advances the Copper's own state. It
	// runs after the journal replay so the frame's CPU writes are in place
	// first; interleaving the two row by row below the display is a separate
	// question the journal does not currently answer.
	//
	// Recording stays suppressed across it. The Copper's writes are the
	// machine's, not the guest's, and the journal exists to replay the guest's:
	// the display loop above gets that for free because it runs inside
	// BeginReplay's window, and this loop has to ask for it. Without that, a
	// MOVE made on a border or blanking line is journalled, and the next
	// frame's BeginReplay undoes it for the whole of that frame's display.
	if u.nextCopper != nil && !u.nextCopper.Idle() {
		resume := func() {}
		if journal != nil {
			resume = journal.SuspendRecording()
		}
		lines := u.linesPerFrame()
		for y := ScreenHeight; y < lines; y++ {
			for hc := 0; hc < columnsPerLine; hc++ {
				u.copperDebt = u.stepCopperColumn(y, hc, u.copperDebt)
			}
		}
		resume()
	}

	if timex {
		// Hand the composed 320-pixel centre to the Timex renderer.
		for r := 0; r < TotalHeight; r++ {
			fy := r + nextFrameRowBias
			copy(u.img.Pix[r*u.img.Stride:r*u.img.Stride+TotalWidth*4], frame.Pix[fy*frame.Stride:fy*frame.Stride+TotalWidth*4])
		}
		return u.renderTimexHiRes()
	}
	if u.nextCompositor.HasActiveSprites() || u.nextCompositor.HiResLayer2Active() {
		return frame
	}
	// The centre 240 lines.
	var out *image.RGBA
	if scale == 1 {
		out = u.img
	} else {
		if u.wideImg == nil {
			u.wideImg = image.NewRGBA(image.Rect(0, 0, outW, TotalHeight))
		}
		out = u.wideImg
	}
	for r := 0; r < TotalHeight; r++ {
		fy := r + nextFrameRowBias
		copy(out.Pix[r*out.Stride:r*out.Stride+outW*4], frame.Pix[fy*frame.Stride:fy*frame.Stride+outW*4])
	}
	return out
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

// SetAudioFrameRate tells the audio output how many frames a second the
// frame loop really runs at, so it plays each frame's samples out over that
// period rather than over a flat 20 ms. No-op without audio.
func (u *ULA) SetAudioFrameRate(hz float64) {
	if u.audio != nil {
		u.audio.SetProducerFrameRate(hz)
	}
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
		if !u.audioHeld {
			u.audio.PushBeeperSamples(make([]int16, audio.SamplesPerFrame))
		}
		if u.mem.TStates != nil {
			u.frameStartTstate = *u.mem.TStates
		}
		return
	}
	samples := u.mixAudioFrame()
	if !u.audioHeld {
		u.audio.PushStereoSamples(samples)
	}
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
	n := u.FrameTStates()
	if u.syncLen > 0 {
		n = u.syncLen // the frame a sync moved the end of
	}
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
