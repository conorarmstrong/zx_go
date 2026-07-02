package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/audiodac"
	"github.com/conorarmstrong/zx_go/pkg/betadisk"
	"github.com/conorarmstrong/zx_go/pkg/config"
	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/next"
	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/next/dac"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/esxdos"
	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nex"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/rzx"
	"github.com/conorarmstrong/zx_go/pkg/sam"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/version"
	"github.com/conorarmstrong/zx_go/pkg/z80"
	"github.com/conorarmstrong/zx_go/pkg/zx8x"
	"github.com/conorarmstrong/zx_go/pkg/zxlog"
)

const (
	// tstatesPerFrame is the 48K ULA frame length. The 128K family and
	// the Next use a longer frame — see frameTStatesForModel, which the
	// ExecuteFrame loops use so the maskable INT cadence is model-correct
	// (timing.md §1a; the Next previously ran the 48K 69888 by mistake).
	tstatesPerFrame = 69888

	// tapeTurboFramesPerTick is how many emulated frames run per 50 Hz tick
	// while fast tape loading is active. ~64× turns a multi-minute real-time
	// tape load into ~10 s of wall-clock without starving the UI thread
	// (the emulation core sustains well over 64 frames per 20 ms tick).
	tapeTurboFramesPerTick = 64

	// tapeAutoPauseTicks is how many consecutive ticks the loader can be idle
	// (no tape-rate $FE reads) before the tape is auto-paused so it doesn't
	// advance past the next multi-load part. The loader polls continuously
	// while loading, so this only triggers when the program is actually
	// running (menu / inter-block processing).
	tapeAutoPauseTicks = 10

	// tapeLoadReadThreshold is the per-frame port-$FE read count above which
	// the CPU is considered to be in a tape loader's edge-timing loop (which
	// polls $FE thousands of times per frame) rather than a running game
	// (which reads it only sparsely, for the keyboard). Gates fast-load turbo.
	tapeLoadReadThreshold = 500
)

// frameTStatesForModel returns the ULA frame length in 3.5 MHz T-states
// for a machine model: 48K = 69888; the 128K family (128K/+2/+2A/+3) and
// the Spectrum Next (which boots in +3/128K timing) = 70908. Matches the
// per-model convention in z80.ExecuteFrame and the 70908 stepFrameBudget
// used by StepInstructionWithIRQ, and the FPGA frame geometry
// (zxula_timing.vhd c_max_hc/c_max_vc → (456·311)/2 = 70908).
func frameTStatesForModel(model roms.SpectrumModel) int {
	switch model {
	case roms.Model48K:
		return tstatesPerFrame // 69888
	case roms.ModelPentagon:
		return 71680 // Pentagon 128: 320 lines × 224 T-states, no contention
	default:
		return 70908
	}
}

type keyState struct {
	key     fyne.KeyName
	pressed bool
}

// JoystickType identifies which joystick interface (if any) is currently
// active. Only one joystick interface can be active at a time.
type JoystickType int

const (
	JoystickNone      JoystickType = iota
	JoystickKempston               // Hardware port 0x1F (handled by ULA)
	JoystickSinclair1              // Sinclair Interface 2 left joystick (keys 1..5)
	JoystickSinclair2              // Sinclair Interface 2 right joystick (keys 6..0)
	JoystickCursor                 // Protek/Cursor joystick (keys 5/6/7/8/0)
)

type emulator struct {
	cpu *z80.CPU
	mem *memory.Memory

	// zx8x is non-nil only for the Sinclair ZX80/ZX81, whose CPU-generated
	// display has no ULA. When set, the render loop reads zx8x.Image() and the
	// ula/peripherals fields are nil.
	zx8x *zx8x.Machine

	// sam is non-nil only for the SAM Coupé, which has its own memory map,
	// I/O/ASIC, keyboard and sound (pkg/sam). When set, the run loop calls
	// sam.RunFrame(), the render loop reads sam.Render(), keys route to sam.Kbd,
	// and the ula/mem/kbd fields hold inert stand-ins (the SAM uses its own).
	sam *sam.Machine

	// samAudio plays the SAM Coupé's SAA1099 output. Created (GUI only, unless
	// --no-sound) when the SAM machine is built; the run loop pushes a mono
	// downmix each frame. nil for every other machine and in headless mode.
	samAudio    *audio.AudioSystem
	samAudioBuf []int16 // reused per-frame mono buffer for samAudio

	// speccyDAC is the classic-Spectrum SpecDrum/Covox 8-bit DAC (non-nil on
	// 48K/128K/+2/+2A/+3; nil on Next/ZX8x). Toggled from the Peripherals menu.
	speccyDAC *audiodac.DAC

	// betaDisk is the Beta Disk / TR-DOS interface, created lazily the first
	// time a .TRD is mounted (classic models only). nil until then.
	betaDisk *betadisk.Interface

	// fastTape, when set, runs the emulation at many frames per tick while a
	// tape is actively loading, so a multi-minute real-time tape load (custom
	// turbo loaders go through the edge-timed ROM/loader loop, which can't be
	// trap-accelerated) finishes in seconds. Toggled from the Tape menu.
	fastTape atomic.Bool

	// tapeReadActive tracks whether the previous tick saw a tape-loader-like
	// rate of port-$FE reads. Turbo only engages while this holds, so once a
	// game stops loading and starts running (e.g. its menu), turbo disengages
	// and the game runs at normal speed with audio — even if the tape still
	// has blocks queued for a later multi-load stage.
	tapeReadActive bool
	tapeIdleTicks  int // consecutive ticks the loader has been idle (auto-pause)

	// SD write-back (opt-in via --sd-writeback): the mounted image
	// source + its file path, so guest writes can be persisted at
	// exit. nil/"" when the flag is off or no file-backed image is
	// mounted.
	sdImageSrc  *sdcard.ImageSource
	sdImagePath string
	ula         *ula.ULA
	kbd         *keyboard.Keyboard
	peripherals *peripherals.PeripheralManager
	// model is the machine this emulator was built for. Used to pick the
	// ULA frame length (frameTStatesForModel) so the maskable INT cadence
	// is model-correct in the GUI render loop too (timing.md §1a).
	model roms.SpectrumModel

	paused atomic.Bool
	ticker *time.Ticker

	// Track physical key states to prevent OS repeat issues
	physicalKeys map[fyne.KeyName]bool
	keyMutex     sync.Mutex

	// Frame counter
	frameCounter int32

	// Window reference for fullscreen toggle
	window fyne.Window

	// Separate goroutine for processing keys
	keyQueue chan keyState
	stopChan chan struct{}

	// CRT scanline filter toggle. When true, the rendered image is upscaled
	// 2x and every other row is darkened to mimic a CRT.
	crtFilter atomic.Bool

	// CRT post-process destination. When the filter is enabled the render
	// goroutine writes the upscaled image here and points screen.Image at
	// it. Sized lazily on first use; reused across frames.
	crtScratch *image.RGBA

	// Currently active joystick interface. Mutated only from the UI thread.
	joystickType JoystickType

	// RZX session state. At most one of rzxPlayback / rzxRecord is
	// non-nil at any given time (FUSE rzx.c:164,278). Atomic so the
	// per-frame read in the emulation goroutine doesn't need a lock,
	// and so menu-thread Set calls don't race the per-IN ULA hook.
	// rzxRecordFilename is only touched from the UI thread (set on
	// Start, read on Stop), so it doesn't need atomic protection.
	rzxPlayback       atomic.Pointer[rzx.Playback]
	rzxRecord         atomic.Pointer[rzx.Recording]
	rzxRecordFilename string

	// Spectrum Next subsystem refs. Non-nil only on ModelNext.
	// nextEsxdos is the RST 8 dispatcher (used to attach an SD
	// mount); nextDAC is the four-channel DAC bank (used to
	// verify mixing). nextRegs is the NextReg file (used by the
	// debugger for read-back). All owned by the Next bus
	// constructed in newNextEmulator.
	nextEsxdos  *esxdos.Dispatcher
	nextDAC     *dac.Bank
	nextRegs    *nextregs.Dispatcher
	nextPalette *palette.Bank
	nextTilemap *tilemap.Tilemap
	nextCopper  *copper.Copper
	nextSprites *sprite.Engine
	nextLayer2  *layer2.Layer2

	// nexloadMacro, when non-nil, drives the NextZXOS .nexload dot
	// command from the run loop to load a .nex via the genuine OS
	// loader (File -> Open). It is advanced once per executed frame and
	// cleared when finished.
	nexloadMacro *nexloadMacro

	// cpuSpeedForce is the user-facing "CPU Speed" override (ModelNext):
	// 0 = Auto (follow the guest's NextReg $07), 1..4 = force 3.5/7/14/28 MHz.
	// The zero value is Auto so no constructor init is needed. NextZXOS runs
	// at 28 MHz by default, which makes some games (e.g. RustHawk) run too
	// fast; forcing 3.5 MHz is the emulator equivalent of the Next's menu
	// speed selector / F8 hotkey. Applied each frame via applyForcedCPUSpeed.
	cpuSpeedForce int

	// debugHistory is the shared M1-fetch ring populated by the
	// CPU pre-fetch hook. Both the telnet `history` / `prev`
	// commands and the visual debugger's History tab read from it.
	// nil until the first surface that needs it (telnet via
	// --debugger-history, or the visual debugger menu) creates it.
	debugHistory *debugger.History

	// debugBreakpoints is the SINGLE shared breakpoint store used by
	// both the telnet and visual debuggers, so a breakpoint set from
	// one surface is visible and active in the other. Lazily created
	// by whichever debugger initialises first (see sharedBreakpoints).
	debugBreakpoints *debugger.BreakpointSet

	// debugRegWatches is the SINGLE shared register-watchpoint set,
	// likewise shared between the telnet and visual debuggers.
	debugRegWatches *debugger.RegWatchSet

	// timeTravel is the rolling snapshot ring, owned by the emulator
	// so BOTH the telnet `tt-*` commands and the visual debugger's
	// Time-Travel tab drive the same buffer. nil when disabled.
	timeTravel *timeTravelBuffer

	// rdbg is the optional ZRCP-style telnet debugger. Constructed
	// in main when --debugger-port>0; nil otherwise. WaitIfPaused
	// is safe on a nil receiver, so the GUI loop can call it
	// unconditionally.
	rdbg *remoteDebugger
}

// sharedBreakpoints returns the emulator's single shared breakpoint
// store, creating it on first use. Both the telnet debugger and the
// visual debugger call this so they operate on the same set.
func (e *emulator) sharedBreakpoints() *debugger.BreakpointSet {
	if e.debugBreakpoints == nil {
		e.debugBreakpoints = debugger.NewBreakpointSet()
	}
	return e.debugBreakpoints
}

// sharedRegWatches returns the emulator's single shared
// register-watchpoint set (telnet `watch-reg` + the GUI Watchpoints
// tab operate on the same instance).
func (e *emulator) sharedRegWatches() *debugger.RegWatchSet {
	if e.debugRegWatches == nil {
		e.debugRegWatches = debugger.NewRegWatchSet()
	}
	return e.debugRegWatches
}

// joystickKeySymbols returns the Spectrum keys (as fyne.KeyName values that
// the emulator's keyboard package recognises) corresponding to up/down/left/
// right/fire for the active joystick. Returns nil if the joystick type
// uses something other than the keyboard matrix (e.g. Kempston).
func joystickKeySymbols(t JoystickType) [5]fyne.KeyName {
	switch t {
	case JoystickSinclair1:
		// Left joystick: 1=left 2=right 3=down 4=up 5=fire
		return [5]fyne.KeyName{fyne.Key4, fyne.Key3, fyne.Key1, fyne.Key2, fyne.Key5}
	case JoystickSinclair2:
		// Right joystick: 6=left 7=right 8=down 9=up 0=fire
		return [5]fyne.KeyName{fyne.Key9, fyne.Key8, fyne.Key6, fyne.Key7, fyne.Key0}
	case JoystickCursor:
		// Cursor joystick: 5=left 6=down 7=up 8=right 0=fire
		// Index order: up, down, left, right, fire
		return [5]fyne.KeyName{fyne.Key7, fyne.Key6, fyne.Key5, fyne.Key8, fyne.Key0}
	}
	return [5]fyne.KeyName{}
}

// joystickKeyForArrow maps a physical arrow / fire key to one of the five
// joystick directions. Returns -1 if the key is not a joystick input.
// Index order matches joystickKeySymbols: 0=up, 1=down, 2=left, 3=right, 4=fire.
func joystickKeyForArrow(name fyne.KeyName) int {
	switch name {
	case fyne.KeyUp:
		return 0
	case fyne.KeyDown:
		return 1
	case fyne.KeyLeft:
		return 2
	case fyne.KeyRight:
		return 3
	case desktop.KeyAltRight, desktop.KeyControlRight:
		return 4
	}
	return -1
}

// applyCRTFilterInto writes a 2x upscaled CRT version of src into dst:
// each input pixel becomes a 2x2 block where the bottom row is halved in
// brightness. dst must have the right dimensions; callers reuse it across
// frames to avoid per-frame allocations.
func applyCRTFilterInto(dst, src *image.RGBA) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < h; y++ {
		srcRow := src.Pix[y*src.Stride : y*src.Stride+w*4]
		topRow := dst.Pix[(y*2)*dst.Stride : (y*2)*dst.Stride+w*8]
		botRow := dst.Pix[(y*2+1)*dst.Stride : (y*2+1)*dst.Stride+w*8]
		for x := 0; x < w; x++ {
			r := srcRow[x*4]
			g := srcRow[x*4+1]
			b := srcRow[x*4+2]
			a := srcRow[x*4+3]
			topRow[x*8+0] = r
			topRow[x*8+1] = g
			topRow[x*8+2] = b
			topRow[x*8+3] = a
			topRow[x*8+4] = r
			topRow[x*8+5] = g
			topRow[x*8+6] = b
			topRow[x*8+7] = a
			r2, g2, b2 := r/2, g/2, b/2
			botRow[x*8+0] = r2
			botRow[x*8+1] = g2
			botRow[x*8+2] = b2
			botRow[x*8+3] = a
			botRow[x*8+4] = r2
			botRow[x*8+5] = g2
			botRow[x*8+6] = b2
			botRow[x*8+7] = a
		}
	}
}

// keyboardWidget implements desktop.Keyable to receive KeyDown/KeyUp
// events, and desktop.Mouseable/desktop.Hoverable to receive mouse
// button and motion events. It sits as a transparent overlay at the
// top of the content stack so it catches events before they reach
// the screen image underneath.
type keyboardWidget struct {
	widget.BaseWidget
	onKeyDown   func(*fyne.KeyEvent)
	onKeyUp     func(*fyne.KeyEvent)
	onTypedRune func(rune)
	onMouseMove func(dx, dy int)
	onMouseBtn  func(btn int, pressed bool)
	onFocusLost func()

	// lastMousePos holds the last MouseMoved position so we can
	// compute deltas. Reset on MouseIn so the first move after
	// re-entering the window doesn't produce a giant jump.
	lastMousePos fyne.Position
	haveLastPos  bool
}

func newKeyboardWidget(onKeyDown, onKeyUp func(*fyne.KeyEvent)) *keyboardWidget {
	kw := &keyboardWidget{
		onKeyDown: onKeyDown,
		onKeyUp:   onKeyUp,
	}
	kw.ExtendBaseWidget(kw)
	return kw
}

func (kw *keyboardWidget) KeyDown(key *fyne.KeyEvent) {
	if kw.onKeyDown != nil {
		kw.onKeyDown(key)
	}
}

func (kw *keyboardWidget) KeyUp(key *fyne.KeyEvent) {
	if kw.onKeyUp != nil {
		kw.onKeyUp(key)
	}
}

func (kw *keyboardWidget) TypedKey(key *fyne.KeyEvent) {
	// Ignore typed keys - we only care about physical key events
}

func (kw *keyboardWidget) TypedRune(r rune) {
	// Typed characters drive layout-independent symbol entry (see
	// emulator.handleTypedRune). Physical keys still drive letters/digits.
	if kw.onTypedRune != nil {
		kw.onTypedRune(r)
	}
}

func (kw *keyboardWidget) FocusGained() {}

// FocusLost fires when the emulator surface loses keyboard focus (window
// switch, dialog, menu). The OS stops delivering key-up events for anything
// currently held, so a held joystick direction or key would otherwise stick on
// (e.g. Sonic kept running right after focus returned). Release everything.
func (kw *keyboardWidget) FocusLost() {
	if kw.onFocusLost != nil {
		kw.onFocusLost()
	}
}

// MouseIn is called when the cursor enters the widget. Reset the
// last-position cache so the first MouseMoved afterwards doesn't
// deliver a stale delta.
func (kw *keyboardWidget) MouseIn(ev *desktop.MouseEvent) {
	kw.haveLastPos = false
}

// MouseOut clears the tracking state — no deltas produced while
// the cursor is outside the window.
func (kw *keyboardWidget) MouseOut() {
	kw.haveLastPos = false
}

// MouseMoved is called on every cursor movement inside the widget.
// We convert the absolute position to deltas against the previous
// sample and forward them to onMouseMove.
func (kw *keyboardWidget) MouseMoved(ev *desktop.MouseEvent) {
	if kw.onMouseMove == nil {
		return
	}
	if kw.haveLastPos {
		dx := int(ev.Position.X - kw.lastMousePos.X)
		dy := int(ev.Position.Y - kw.lastMousePos.Y)
		if dx != 0 || dy != 0 {
			kw.onMouseMove(dx, dy)
		}
	}
	kw.lastMousePos = ev.Position
	kw.haveLastPos = true
}

// MouseDown / MouseUp forward button press/release events to the
// onMouseBtn callback. Button index follows FUSE's Kempston
// convention: 0 = right button, 1 = left button.
func (kw *keyboardWidget) MouseDown(ev *desktop.MouseEvent) {
	if kw.onMouseBtn == nil {
		return
	}
	switch ev.Button {
	case desktop.MouseButtonPrimary:
		kw.onMouseBtn(1, true) // left → bit 1
	case desktop.MouseButtonSecondary:
		kw.onMouseBtn(0, true) // right → bit 0
	}
}

func (kw *keyboardWidget) MouseUp(ev *desktop.MouseEvent) {
	if kw.onMouseBtn == nil {
		return
	}
	switch ev.Button {
	case desktop.MouseButtonPrimary:
		kw.onMouseBtn(1, false)
	case desktop.MouseButtonSecondary:
		kw.onMouseBtn(0, false)
	}
}

func (kw *keyboardWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewWithoutLayout())
}

var (
	_ desktop.Keyable   = (*keyboardWidget)(nil)
	_ desktop.Mouseable = (*keyboardWidget)(nil)
	_ desktop.Hoverable = (*keyboardWidget)(nil)
)

// savePlus3Disk opens a file save picker and writes the current disk in
// the given drive to a DSK file.
func savePlus3Disk(emu *emulator, w fyne.Window, drive int) {
	fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if writer == nil {
			return
		}
		path := writer.URI().Path()
		_ = writer.Close()
		if err := emu.peripherals.SavePlus3Disk(drive, path); err != nil {
			dialog.ShowError(fmt.Errorf("failed to save DSK: %w", err), w)
			return
		}
		driveName := "A"
		if drive == 1 {
			driveName = "B"
		}
		dialog.ShowInformation("Disk Saved",
			"Wrote drive "+driveName+" to "+filepath.Base(path)+".", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".dsk"}))
	fd.Show()
}

// insertInterface2Cartridge opens a file picker for a 16KB .rom
// cartridge image and inserts it into the Interface 2 cartridge
// slot. Refuses (with explanation) on non-48K models, since the
// IF2 only worked on the original 48K Spectrum. On success, the
// emulator is rebooted so the cartridge code starts executing
// from PC=0x0000.
func insertInterface2Cartridge(emu *emulator, w fyne.Window, currentModel roms.SpectrumModel) {
	if currentModel != roms.Model48K {
		dialog.ShowInformation("Interface 2",
			"Interface 2 ROM cartridges only work on the 48K Spectrum.\n"+
				"Switch the machine model from the Machine menu first.", w)
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		if err := emu.peripherals.InsertInterface2Cartridge(path); err != nil {
			dialog.ShowError(fmt.Errorf("failed to insert cartridge: %w", err), w)
			return
		}
		emu.reboot()
		dialog.ShowInformation("Cartridge Inserted",
			"Inserted "+emu.peripherals.Interface2CartridgeName()+
				".\n\nThe emulator has been rebooted into the cartridge.", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".rom"}))
	fd.Show()
}

// ejectInterface2Cartridge removes any inserted Interface 2
// cartridge and reboots into the normal BASIC ROM.
func ejectInterface2Cartridge(emu *emulator, w fyne.Window) {
	if !emu.peripherals.IsInterface2CartridgeInserted() {
		dialog.ShowInformation("Interface 2",
			"No cartridge is currently inserted.", w)
		return
	}
	name := emu.peripherals.Interface2CartridgeName()
	emu.peripherals.RemoveInterface2Cartridge()
	emu.reboot()
	dialog.ShowInformation("Cartridge Ejected",
		"Ejected "+name+". The emulator has been rebooted into BASIC.", w)
}

// loadDiscipleDisk opens a file picker for an MGT/IMG/SAD disk image and
// mounts it in the DISCiPLE's drive (0 = 1, 1 = 2).
func loadDiscipleDisk(emu *emulator, w fyne.Window, drive int) {
	if !emu.peripherals.IsDiscipleEnabled() {
		dialog.ShowInformation("Load DISCiPLE Disk",
			"Enable the DISCiPLE interface first from the Peripherals menu.", w)
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		if err := emu.peripherals.LoadDiscipleDisk(drive, path); err != nil {
			dialog.ShowError(fmt.Errorf("failed to load disk: %w", err), w)
			return
		}
		slog.Info("disk inserted", "interface", "DISCiPLE", "drive", drive+1, "path", path)
		dialog.ShowInformation("Disk Loaded",
			"Inserted "+filepath.Base(path)+" into DISCiPLE drive "+fmt.Sprintf("%d", drive+1)+".", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{
		".mgt", ".img", ".sad", ".dsk", ".trd", ".d40", ".d80",
	}))
	fd.Show()
}

// loadPlus3Disk opens a file picker for a DSK image and mounts it in the
// given +3 FDC drive (0 = A, 1 = B). Refuses (with explanation) on
// non-+3/+2A models.
func loadPlus3Disk(emu *emulator, w fyne.Window, currentModel roms.SpectrumModel, drive int) {
	if currentModel != roms.ModelPlus3 && currentModel != roms.ModelPlus2A {
		dialog.ShowInformation("Load Disk",
			"DSK images can only be loaded on the +3 or +2A.\n"+
				"Switch the machine model from the Machine menu first.", w)
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		if err := emu.peripherals.LoadPlus3Disk(drive, path); err != nil {
			dialog.ShowError(fmt.Errorf("failed to load DSK: %w", err), w)
			return
		}
		driveName := "A"
		if drive == 1 {
			driveName = "B"
		}
		slog.Info("disk inserted", "interface", "+3", "drive", driveName, "path", path)
		dialog.ShowInformation("Disk Loaded",
			"Inserted "+filepath.Base(path)+" into drive "+driveName+".", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{
		".dsk", ".udi", ".mgt", ".img", ".trd", ".sad", ".d40", ".d80",
	}))
	fd.Show()
}

// loadTRDDisk shows a .TRD file picker and mounts the chosen image in the given
// Beta drive (0 = A, 1 = B), enabling TR-DOS on first use.
func loadTRDDisk(emu *emulator, w fyne.Window, drive int) {
	if !emu.betaSupported() {
		dialog.ShowInformation("Load TR-DOS Disk",
			"TR-DOS disks run on the Pentagon 128 (and the 48K/128K).\n"+
				"Switch the machine model from the Machine menu first.", w)
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		var mountErr error
		_ = emu.withEmulationPaused(func() error {
			mountErr = emu.mountTRD(drive, path)
			return nil
		})
		if mountErr != nil {
			dialog.ShowError(fmt.Errorf("failed to load TRD: %w", mountErr), w)
			return
		}
		driveName := "A"
		if drive == 1 {
			driveName = "B"
		}
		slog.Info("disk inserted", "interface", "TR-DOS", "drive", driveName, "path", path)
		dialog.ShowInformation("TR-DOS Disk Loaded",
			"Inserted "+filepath.Base(path)+" into TR-DOS drive "+driveName+".\n"+
				"From BASIC, enter TR-DOS with: RANDOMIZE USR 15616  (or the 128 menu).", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".trd"}))
	fd.Show()
}

// loadSAMDisk shows an MGT/SAD/DSK file picker and inserts the chosen image into
// the given SAM Coupé drive (0 = drive 1, 1 = drive 2). SAM-only.
func loadSAMDisk(emu *emulator, w fyne.Window, drive int) {
	if emu.sam == nil {
		dialog.ShowInformation("Load SAM Disk",
			"SAM disks run on the SAM Coupé.\n"+
				"Switch to it from the Machine menu first.", w)
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			dialog.ShowError(fmt.Errorf("failed to read disk: %w", readErr), w)
			return
		}
		disk, derr := sam.LoadDisk(data)
		if derr != nil {
			dialog.ShowError(fmt.Errorf("failed to load SAM disk: %w", derr), w)
			return
		}
		_ = emu.withEmulationPaused(func() error {
			emu.sam.InsertDisk(drive, disk)
			return nil
		})
		slog.Info("disk inserted", "interface", "SAM", "drive", drive+1, "path", path)
		dialog.ShowInformation("Disk Loaded",
			fmt.Sprintf("Inserted %s into SAM drive %d.\n"+
				"From SAM BASIC, boot it with:  BOOT  (or load with LOAD).",
				filepath.Base(path), drive+1), w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".mgt", ".sad", ".dsk", ".img"}))
	fd.Show()
}

// userKeymapPath returns the absolute path to the user's keymap override
// file under the platform-appropriate config dir: keymap.json inside
// `<os.UserConfigDir>/zx_go/` (= `%AppData%\zx_go\keymap.json` on
// Windows, `~/Library/Application Support/zx_go/keymap.json` on macOS,
// `$XDG_CONFIG_HOME/zx_go/keymap.json` or `~/.config/zx_go/keymap.json`
// on Linux). Matches the convention used by `pkg/config/config.go::Path()`
// for `config.json`. Does not create the file or directory; the caller
// decides whether missing files matter.
func userKeymapPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cfg, "zx_go", "keymap.json")
}

// configureClassicIntTiming sets the maskable frame interrupt to the
// hardware-faithful narrow pulse for 128K-family models, replacing the legacy
// "held for the whole frame" model. The held model re-fires the INT as soon as
// interrupts are re-enabled, so when a game's ISR (or a DI section) spans the
// frame boundary, it takes an interrupt that real hardware MISSES — drifting
// timing-sensitive code (the cause of garbled sprites in Ghouls 'n' Ghosts).
// Pulse/assert values come from next.FrameIntTiming, already validated against
// a reference emulator. 48K keeps the legacy held model. The Next gets the same
// narrow pulse (it boots in +3/128K timing, NR$03 "011") — this is what the
// Machine-menu switch path relies on, since otherwise the Next inherits the
// previous model's INT mode (e.g. 48K held), re-firing the frame INT across
// DI'd ISR boundaries and garbling software sprites in 128K personality.
// Opt out for A/B with ZX_GO_NO_INT_TIMING.
func configureClassicIntTiming(cpu *z80.CPU, model roms.SpectrumModel) {
	if cpu == nil {
		return
	}
	var nr03 byte
	switch model {
	case roms.Model128K, roms.ModelPlus2:
		nr03 = 0x02
	case roms.ModelPlus3, roms.ModelPlus2A:
		nr03 = 0x03
	case roms.ModelPentagon:
		nr03 = 0x04
	case roms.ModelNext:
		nr03 = 0x03 // NextZXOS boots in +3/128K timing (NR$03 default "011")
	default: // 48K and others: keep the legacy held-INT model.
		cpu.IntAssertTstate, cpu.IntPulseTstates = 0, 0
		return
	}
	if os.Getenv("ZX_GO_NO_INT_TIMING") != "" {
		cpu.IntAssertTstate, cpu.IntPulseTstates = 0, 0
		return
	}
	assert, pulse := next.FrameIntTiming(nr03, false)
	cpu.IntAssertTstate = uint64(assert)
	cpu.IntPulseTstates = uint64(pulse)
}

func newEmulator(model roms.SpectrumModel) (*emulator, error) {
	if model == roms.ModelNext {
		return newNextEmulator()
	}
	if isZX8x(model) {
		return newZX8xEmulator(model)
	}
	if model == roms.ModelSAM {
		e, err := newSamEmulator()
		if err != nil {
			return nil, err
		}
		// Enable SAA1099 audio (GUI path only; runSAMHeadless calls
		// newSamEmulator directly and stays silent). --no-sound opts out.
		if cliFlagsActive == nil || !cliFlagsActive.noSound {
			if as, aerr := audio.New(); aerr != nil {
				slog.Warn("sam: audio init failed", "err", aerr)
			} else if serr := as.Start(); serr != nil {
				slog.Warn("sam: audio start failed", "err", serr)
				_ = as.Close()
			} else {
				e.samAudio = as
			}
		}
		return e, nil
	}
	kbd := keyboard.New()
	if path := userKeymapPath(); path != "" {
		if err := kbd.LoadOverrides(path); err != nil {
			slog.Warn("failed to load custom keymap", "err", err)
		}
	}
	mem, err := memory.New("roms", model)
	if err != nil {
		return nil, err
	}
	ula := ula.New(mem, kbd)
	cpu := z80.New(mem, ula)
	configureClassicIntTiming(cpu, model)

	// Classic-Spectrum SpecDrum/Covox DAC (both off until enabled from the
	// Peripherals menu). Event-timed and mixed into the beeper by the ULA.
	speccyDAC := audiodac.New()
	ula.SetSpeccyDAC(speccyDAC)

	// Initialize audio unless --no-sound was passed
	if cliFlagsActive == nil || !cliFlagsActive.noSound {
		ula.EnableAudio()
		configureAudioExtras(ula)
	} else {
		slog.Info("--no-sound: audio disabled")
	}

	// Create peripheral manager and wire it to ULA and memory
	pm := peripherals.NewPeripheralManager(mem, "roms")
	ula.SetPeripherals(pm)
	mem.PeripheralRead = pm.HandleMemoryRead
	mem.PeripheralWrite = pm.HandleMemoryWrite

	// NMI: keyboard goroutine sets a flag, CPU processes it on the emulator
	// goroutine. The NMICallback pages in the Multiface ROM at the exact
	// moment the NMI fires (not before, which would corrupt execution).
	kbd.SetNMICallback(func() {
		if pm.IsMultifaceEnabled() {
			cpu.PendingNMI.Store(true)
		}
	})
	cpu.NMICallback = func() {
		if pm.IsMultifaceEnabled() {
			pm.HandleNMI() // Pages in Multiface ROM
		}
	}

	e := &emulator{
		cpu:          cpu,
		mem:          mem,
		ula:          ula,
		kbd:          kbd,
		peripherals:  pm,
		speccyDAC:    speccyDAC,
		model:        model,
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10),
		stopChan:     make(chan struct{}),
	}
	e.paused.Store(true)
	e.fastTape.Store(true) // accelerate tape loading by default
	return e, nil
}

// configureAudioExtras applies post-EnableAudio CLI options: the keep-alive
// dither level override and startup WAV capture.
func configureAudioExtras(u *ula.ULA) {
	if cliFlagsActive != nil && cliFlagsActive.audioKeepAlive >= 0 {
		u.SetAudioKeepAliveLevel(int16(cliFlagsActive.audioKeepAlive))
	}
	if cliFlagsActive != nil {
		u.SetDCBlockEnabled(cliFlagsActive.audioDCBlock)
	}
	maybeStartAudioRecording(u)
}

// maybeStartAudioRecording begins WAV capture at startup when
// --record-audio <path> was supplied. The captured file is the exact
// mixed mono stream we hand to the audio device, so it's ground truth for
// diagnosing clicks/pops that only manifest live.
func maybeStartAudioRecording(u *ula.ULA) {
	if cliFlagsActive == nil || cliFlagsActive.recordAudio == "" {
		return
	}
	if err := u.StartRecording(cliFlagsActive.recordAudio); err != nil {
		slog.Error("--record-audio: failed to start recording", "path", cliFlagsActive.recordAudio, "err", err)
		return
	}
	slog.Info("--record-audio: capturing mixed audio output", "path", cliFlagsActive.recordAudio)
}

// tapeLoadingActive reports whether a tape is mid-load (playing with blocks
// still to load) — the window in which fast-tape turbo applies.
func (e *emulator) tapeLoadingActive() bool {
	if e.ula == nil {
		return false
	}
	tp := e.ula.GetTapePlayer()
	return tp != nil && tp.IsPlaying() && tp.HasMoreBlocks()
}

// tapeAudioMuted reports whether beeper audio should be muted because a
// fast-tape load is in progress. Crucially this is decoupled from the per-tick
// turbo burst: muting spans the WHOLE load — including the brief inter-block
// gaps where the loader isn't reading edges this tick — so the output doesn't
// blip on/off at every block boundary (the "stuttering noise" during a
// multi-block load). When fast-tape is disabled the tape loads at 1x with its
// authentic loading sound, so nothing is muted. Once the loader goes idle and
// the tape auto-pauses, this returns false and the game's music plays.
func (e *emulator) tapeAudioMuted() bool {
	return e.fastTape.Load() && e.tapeLoadingActive()
}

func (e *emulator) startKeyProcessor() {
	// Separate goroutine to process keyboard events
	// This prevents any blocking of the UI thread
	go func() {
		for {
			select {
			case ks := <-e.keyQueue:
				// Process the key state change
				if e.sam != nil {
					e.samSetKey(ks.key, ks.pressed)
				} else {
					e.kbd.HandleKeyWithModifiers(ks.key, ks.pressed, false, false, false, false)
				}
			case <-e.stopChan:
				return
			}
		}
	}()
}

func (e *emulator) handleKeyDown(ev *fyne.KeyEvent) {
	// Escape exits fullscreen
	if ev.Name == fyne.KeyEscape && e.window != nil && e.window.FullScreen() {
		e.window.SetFullScreen(false)
		return
	}

	// Quick save (F2) / quick load (F4) state slot.
	switch ev.Name {
	case fyne.KeyF2:
		if err := e.quickSaveState(); err != nil {
			slog.Warn("quick-save failed", "err", err)
		} else {
			slog.Info("quick-saved state", "path", quickSavePath())
		}
		return
	case fyne.KeyF4:
		if err := e.quickLoadState(); err != nil {
			slog.Warn("quick-load failed", "err", err)
		} else {
			slog.Info("quick-loaded state")
		}
		return
	}

	e.keyMutex.Lock()

	// Check if this is a repeat event from the OS
	if e.physicalKeys[ev.Name] {
		e.keyMutex.Unlock()
		return // Ignore repeat
	}
	e.physicalKeys[ev.Name] = true
	e.keyMutex.Unlock()

	// Joystick interception. The arrow keys / right modifiers are routed to
	// whichever joystick interface is currently active and are NOT
	// forwarded to the keyboard matrix in the usual way.
	if e.effectiveJoystick() != JoystickNone {
		idx := joystickKeyForArrow(ev.Name)
		if idx >= 0 {
			e.dispatchJoystick(idx, true)
			return
		}
	}

	// Queue the key event (non-blocking)
	select {
	case e.keyQueue <- keyState{key: ev.Name, pressed: true}:
	default:
		// If queue is full, drop the event (shouldn't happen with normal typing)
	}
}

// effectiveJoystickFor resolves the joystick scheme that should handle arrow
// keys for a given configured choice and machine model. An unconfigured
// (None) joystick on the Spectrum Next falls back to Kempston: the Next FPGA
// always decodes the Kempston port ($1F) and nearly all Next software reads
// it, so without this a fresh user's arrows drive nothing and Kempston-only
// games (e.g. Sonic) are uncontrollable. Any explicit choice is preserved,
// and every other model keeps None (the user opts in via Peripherals menu).
func effectiveJoystickFor(configured JoystickType, model roms.SpectrumModel) JoystickType {
	if configured == JoystickNone && model == roms.ModelNext {
		return JoystickKempston
	}
	return configured
}

// effectiveJoystick is effectiveJoystickFor applied to the live model.
func (e *emulator) effectiveJoystick() JoystickType {
	model := roms.Model48K
	if e.mem != nil {
		model = e.mem.GetCurrentModel()
	}
	return effectiveJoystickFor(e.joystickType, model)
}

// applyForcedCPUSpeed enforces the user's "CPU Speed" override (cpuSpeedForce)
// on the live CPU. Called once per executed frame:
//   - Auto (0), no CPU, or a .nex still loading (macro active) -> release any
//     lock so NextZXOS boots/loads at its own (28 MHz) speed and guest NR$07
//     writes apply normally;
//   - otherwise pin the CPU to the chosen speed (1..4 -> selector 0..3), so a
//     game NextZXOS would run too fast at 28 MHz can be played at 3.5 MHz.
//
// Idempotent, so it survives the reboot that File -> Open performs.
func (e *emulator) applyForcedCPUSpeed() {
	if e.cpu == nil {
		return
	}
	if e.cpuSpeedForce <= 0 || e.nexloadMacro != nil {
		e.cpu.UnlockSpeedSelect()
		return
	}
	e.cpu.LockSpeedSelect(byte(e.cpuSpeedForce - 1))
}

// cpuSpeedMenuItem builds the Machine -> "CPU Speed" submenu. NextZXOS runs the
// Next at 28 MHz by default, which makes some games (e.g. RustHawk) run far too
// fast; these options pin the CPU speed — the emulator equivalent of the Next's
// on-screen menu speed selector (left/right arrows) and F8 speed hotkey. "Auto"
// follows whatever the guest/OS selects (NextReg $07). The choice is applied
// once per frame by applyForcedCPUSpeed and survives the File->Open reboot.
func cpuSpeedMenuItem(emu *emulator) *fyne.MenuItem {
	set := func(force int) func() {
		return func() { emu.cpuSpeedForce = force }
	}
	parent := fyne.NewMenuItem("CPU Speed (Next)", nil)
	parent.ChildMenu = fyne.NewMenu("",
		fyne.NewMenuItem("Auto (follow game/OS)", set(0)),
		fyne.NewMenuItem("3.5 MHz", set(1)),
		fyne.NewMenuItem("7 MHz", set(2)),
		fyne.NewMenuItem("14 MHz", set(3)),
		fyne.NewMenuItem("28 MHz", set(4)),
	)
	return parent
}

// dispatchJoystick translates a joystick direction (0=up..4=fire) into the
// appropriate hardware action for the active joystick interface. For
// Kempston this sets/clears a port bit; for Sinclair/Cursor it injects a
// Spectrum key press into the keyboard matrix.
func (e *emulator) dispatchJoystick(direction int, pressed bool) {
	switch e.effectiveJoystick() {
	case JoystickKempston:
		var mask byte
		switch direction {
		case 0:
			mask = ula.KempstonUp
		case 1:
			mask = ula.KempstonDown
		case 2:
			mask = ula.KempstonLeft
		case 3:
			mask = ula.KempstonRight
		case 4:
			mask = ula.KempstonFire
		}
		if mask != 0 && e.ula != nil {
			e.ula.SetKempstonButton(mask, pressed)
		}
	case JoystickSinclair1, JoystickSinclair2, JoystickCursor:
		keys := joystickKeySymbols(e.joystickType)
		key := keys[direction]
		if key == "" {
			return
		}
		e.kbd.HandleKeyWithModifiers(key, pressed, false, false, false, false)
	}
}

// handleTypedRune injects a typed symbol character (e.g. '.', ';', ':') into
// the Spectrum keyboard as a layout-independent SYMBOL-SHIFT combination — so
// symbols type correctly whatever the host keyboard layout (a French AZERTY
// full stop is Shift+';'-key, which the physical key path can't express).
// Letters/digits are ignored here (TypeRune returns false) and keep coming from
// the physical key path so games can hold them. The ZX80/81 use a different
// keyword keyboard, so this is Spectrum/Next-only.
func (e *emulator) handleTypedRune(r rune) {
	if e.paused.Load() {
		return
	}
	// The SAM has its own keyboard with its own symbol layout.
	if e.sam != nil {
		e.sam.Kbd.TypeRune(r)
		return
	}
	if e.kbd == nil || e.zx8x != nil {
		return
	}
	e.kbd.TypeRune(r)
}

func (e *emulator) handleKeyUp(ev *fyne.KeyEvent) {
	e.keyMutex.Lock()
	wasPressed := e.physicalKeys[ev.Name]
	delete(e.physicalKeys, ev.Name)
	e.keyMutex.Unlock()

	// Joystick interception (release). Always release the direction — even if
	// the matching key-down was never recorded (a dropped event, or the
	// key-down arrived while physicalKeys had been cleared). Releasing a
	// direction that isn't held is harmless, and skipping it is exactly what
	// left the Kempston "right" bit stuck (Sonic kept running rightward).
	if e.effectiveJoystick() != JoystickNone {
		idx := joystickKeyForArrow(ev.Name)
		if idx >= 0 {
			e.dispatchJoystick(idx, false)
			return
		}
	}

	// For ordinary matrix keys, only queue a release if we recorded the press
	// (avoids spurious releases for keys we never saw go down).
	if !wasPressed {
		return
	}

	// Queue the key event (non-blocking)
	select {
	case e.keyQueue <- keyState{key: ev.Name, pressed: false}:
	default:
		// If queue is full, drop the event
	}
}

// releaseAllInput lifts every held key and joystick direction. The OS stops
// delivering key-up events when the surface loses focus (or on reboot), so
// anything held would otherwise stick on — e.g. a held Kempston "right" left
// Sonic running rightward after focus returned. Clears the host-key tracking,
// the pending key queue, the Kempston joystick bits, and the keyboard matrix.
func (e *emulator) releaseAllInput() {
	e.keyMutex.Lock()
	e.physicalKeys = make(map[fyne.KeyName]bool)
	e.keyMutex.Unlock()
	for len(e.keyQueue) > 0 {
		<-e.keyQueue
	}
	if e.ula != nil {
		e.ula.KempstonState = 0
	}
	if e.kbd != nil {
		e.kbd.ReleaseAll()
	}
}

func (e *emulator) run(a fyne.App, screen *canvas.Image) {
	// Start the key processor goroutine
	e.startKeyProcessor()

	// Main emulation loop - completely independent of UI events
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond) // 50Hz
		defer ticker.Stop()

		frameCount := 0
		lastRender := time.Now()

		for {
			select {
			case <-ticker.C:
				if !e.paused.Load() {
					// Honour the remote debugger's pause state.
					// WaitIfPaused is nil-safe and a no-op when
					// not paused, so the GUI hot path is unaffected
					// when --debugger-port wasn't supplied.
					e.rdbg.WaitIfPaused()
					// Execution paths: ZX80/ZX81 (CPU-generated video),
					// RZX playback, RZX recording, or normal frame.
					switch {
					case e.zx8x != nil:
						e.zx8x.RunFrame()
					case e.sam != nil:
						e.sam.RunFrame()
						if e.samAudio != nil {
							if e.samAudioBuf == nil {
								e.samAudioBuf = make([]int16, sam.SamplesPerFrame)
							}
							e.sam.GenerateAudioMono(e.samAudioBuf)
							e.samAudio.PushBeeperSamples(e.samAudioBuf)
						}
					case e.rzxPlayback.Load() != nil:
						playback := e.rzxPlayback.Load()
						e.cpu.ExecuteRZXFrame(uint64(playback.Instructions()))
						snapBlock, err := playback.Frame()
						switch {
						case errors.Is(err, rzx.ErrPlaybackFinished):
							e.stopRZXPlayback()
						case err != nil:
							slog.Error("RZX playback error", "err", err)
							e.stopRZXPlayback()
						case snapBlock != nil:
							// Intermediate snapshot — apply it before the next frame.
							snap, derr := rzx.DecodeSnapshot(snapBlock)
							if derr != nil {
								slog.Error("RZX intermediate snapshot decode failed; stopping playback", "err", derr)
								e.stopRZXPlayback()
								break
							}
							if aerr := applySnapshotToEmulator(e, snap); aerr != nil {
								slog.Error("RZX intermediate snapshot apply failed; stopping playback", "err", aerr)
								e.stopRZXPlayback()
							}
						}
					case e.rzxRecord.Load() != nil:
						recorder := e.rzxRecord.Load()
						before := e.cpu.InstructionCount()
						e.cpu.ExecuteFrame(frameTStatesForModel(e.model))
						delta := e.cpu.InstructionCount() - before
						if delta > 0xFFFF {
							slog.Warn("RZX record: frame instruction count exceeded 0xFFFF, clamping", "count", delta)
							delta = 0xFFFF
						}
						if err := recorder.StoreFrame(uint16(delta)); err != nil {
							slog.Error("RZX record StoreFrame", "err", err)
						}
						if recorder.AutosaveDue() {
							if snap, err := createSnapshotFromEmulator(e); err == nil {
								if block, err := rzx.EncodeSnapshot(snap, rzx.SnapshotFormatSZX, true); err == nil {
									recorder.AddAutosave(block, uint32(e.cpu.Tstates()))
								}
							}
						}
					default:
						// Fast tape loading: while a tape is actively loading,
						// run a burst of frames per tick so a real-time load
						// (custom turbo loaders go through the edge-timed loop
						// and can't be trap-accelerated) finishes in seconds
						// instead of minutes. Rendering still happens at 50 Hz.
						n := 1
						// Turbo only while a loader is actively reading tape
						// edges (high $FE read rate last tick) — not merely while
						// the tape has blocks left. Otherwise a multi-load game
						// keeps running at 64x at its menu (rapid attract cycling)
						// with audio muted.
						turbo := e.fastTape.Load() && e.tapeLoadingActive() && e.tapeReadActive
						if turbo {
							n = tapeTurboFramesPerTick
						}
						// Mute for the whole fast-tape load, not just the turbo
						// ticks: at every inter-block gap the read rate dips and
						// turbo would disengage for a tick, leaking one garbled
						// mid-load frame + re-arming the DC blocker — an audible
						// blip at each block boundary (the multi-load "stutter").
						// tapeAudioMuted stays true across those gaps; it clears
						// only when the tape auto-pauses (so music plays) or when
						// fast-tape is off (so the real loading sound is audible).
						if e.ula != nil {
							e.ula.SetFastLoad(e.tapeAudioMuted())
						}
						heavyReads := false
						for k := 0; k < n; k++ {
							var before uint64
							if e.ula != nil {
								before = e.ula.FEReadCount()
							}
							e.cpu.ExecuteFrame(frameTStatesForModel(e.model))
							if e.ula != nil && e.ula.FEReadCount()-before > tapeLoadReadThreshold {
								heavyReads = true
							}
							if k+1 < n && e.peripherals != nil {
								e.peripherals.Frame()
							}
						}
						// Drive next tick's turbo decision from this tick's read
						// rate. The first loading tick runs at 1x (n=1) and sees
						// the heavy reads, so turbo engages on the next tick.
						e.tapeReadActive = heavyReads

						// Loader-activity auto-pause: while the running program is
						// not reading tape edges (a multi-load game's menu, or
						// inter-block processing), pause the tape so it doesn't
						// advance past the next part — which would mis-load it
						// (garbled audio, no music). Resume the instant the loader
						// starts reading again. This also stops the residual
						// loading sound once a part has finished loading.
						if e.ula != nil && os.Getenv("ZX_GO_NO_TAPE_AUTOPAUSE") == "" {
							if tp := e.ula.GetTapePlayer(); tp != nil && tp.HasMoreBlocks() {
								if heavyReads {
									e.tapeIdleTicks = 0
									if !tp.IsPlaying() {
										tp.Resume()
									}
								} else if tp.IsPlaying() {
									e.tapeIdleTicks++
									if e.tapeIdleTicks > tapeAutoPauseTicks {
										tp.Stop()
									}
								}
							}
						}
					}

					frameCount++
					atomic.AddInt32(&e.frameCounter, 1)
					// Advance the typed-character symbol pulse (no-op when idle).
					if e.kbd != nil {
						e.kbd.Tick()
					}
					if e.peripherals != nil {
						e.peripherals.Frame()
					}

					// Advance the NextZXOS .nexload driver (File -> Open),
					// one step per executed frame; keys it presses are seen
					// by the next frame's keyboard scan.
					if e.nexloadMacro != nil {
						if e.nexloadMacro.tick(e) {
							e.nexloadMacro = nil
						}
					}

					// Enforce the user's CPU-speed override (no-op when Auto or
					// still loading) — see applyForcedCPUSpeed.
					e.applyForcedCPUSpeed()

					// Render at 50Hz
					now := time.Now()
					if now.Sub(lastRender) >= 20*time.Millisecond {
						newImage := e.renderFrame()

						// In plain mode, screen.Image already points at the
						// ULA's frame buffer (set at startup) and Render
						// mutated it in place — we just need to refresh.
						// In CRT mode we post-process into a 2x scratch
						// buffer and point screen.Image at that instead.
						displayImg := newImage
						if e.crtFilter.Load() {
							b := newImage.Bounds()
							want := image.Rect(0, 0, b.Dx()*2, b.Dy()*2)
							if e.crtScratch == nil || e.crtScratch.Bounds() != want {
								e.crtScratch = image.NewRGBA(want)
							}
							applyCRTFilterInto(e.crtScratch, newImage)
							displayImg = e.crtScratch
						}

						// Update UI on main thread
						fyne.Do(func() {
							if screen.Image != displayImg {
								screen.Image = displayImg
							}
							screen.Refresh()
						})

						lastRender = now
					}

				}
			case <-e.stopChan:
				return
			}
		}
	}()
}

func (e *emulator) reboot() {
	slog.Info("rebooting emulator")
	if e.zx8x != nil {
		e.zx8x.Reset()
		return
	}
	if e.sam != nil {
		e.sam.Reset()
		return
	}
	e.cpu.Reset()
	e.ula.Reset()

	// On ModelNext, drop the NextReg file back to its power-on
	// values BEFORE memory.Reset re-enables the FPGA bootrom. The
	// dispatcher's Reset drives every OnWrite handler so the
	// tilemap layer, Layer 2, MMU8, palette and divMMC pager also
	// fall back to power-on state — otherwise the previous boot's
	// tilemap stays enabled and renders stale bank-5 data over the
	// fresh boot output as vertical-stripe corruption.
	if e.nextRegs != nil {
		e.nextRegs.Reset()
	}
	// The palette bank carries the NR$44 two-byte write latch
	// (pending9/have9). nextRegs.Reset's Phase 1 zero-pass writes a
	// single 0 byte to NR$44 which leaves the latch in "got high,
	// awaiting low" state — the next boot.bin NR$44 write would
	// then commit (high=0, low=guest_val&1) and corrupt every
	// subsequent palette write, producing the all-blue testcard
	// reported on reboot. Reset the latch AFTER nextRegs.Reset so
	// the half-pair set during Phase 1 is cleared.
	if e.nextPalette != nil {
		e.nextPalette.ResetWriteLatches()
	}

	// Restore paging to the model's power-on defaults. Without this,
	// 128K/+2A/+3 reboots inherit whatever banks/ROM the previously
	// running program had selected — e.g. after loading a +3 .dsk the
	// boot ROM index is no longer 0, so the +3 main menu never
	// reappears and BASIC starts up over corrupted system variables.
	// On ModelNext, memory.Reset re-activates the FPGA bootrom (so
	// the loader runs again), then setupNext rewires the 128K-style
	// page map. Both are required to make Reboot reproduce a cold
	// power-on for the Next.
	if err := e.mem.Reset(); err != nil {
		slog.Error("memory reset failed", "err", err)
	}

	// If the DISCiPLE is enabled, page it in so its boot code at
	// ROM 0x0000 runs on the next frame (cold boot).
	if e.peripherals.IsDiscipleEnabled() {
		if disc := e.peripherals.GetDisciple(); disc != nil {
			disc.PageIn()
		}
	}

	// Clear key states on reboot (host keys, queue, joystick, and matrix —
	// see releaseAllInput: a held key/joystick must not survive the reset).
	e.keyMutex.Lock()
	e.physicalKeys = make(map[fyne.KeyName]bool)
	e.keyMutex.Unlock()

	// Drain key queue
	for len(e.keyQueue) > 0 {
		<-e.keyQueue
	}
	if e.ula != nil {
		e.ula.KempstonState = 0
	}
	if e.kbd != nil {
		e.kbd.ReleaseAll()
	}

	// Re-apply warm-boot on ModelNext if (and ONLY if) the user
	// explicitly opted into the warm-boot debug shortcut. Per the
	// project's "no hacks" rule, the captured-state replay must be
	// opt-in only — see also cmd/zx_go/next.go for the parallel
	// gating during initial boot.
	//
	// Triggers (any of):
	//   - --warm-boot CLI flag       (cliFlagsActive.warmBoot)
	//   - $ZX_GO_WARM_BOOT=1         (env var)
	//
	// $ZX_GO_NO_WARM_BOOT=1 still honoured as a force-off override
	// for backward compatibility.
	warmBootOptedIn := os.Getenv("ZX_GO_WARM_BOOT") != "" ||
		(cliFlagsActive != nil && cliFlagsActive.warmBoot)
	if os.Getenv("ZX_GO_NO_WARM_BOOT") != "" {
		warmBootOptedIn = false
	}
	if warmBootOptedIn && e.cpu != nil && e.mem != nil && e.nextRegs != nil &&
		e.mem.GetCurrentModel() == roms.ModelNext {
		if err := applyWarmBoot(e.cpu, e.mem, e.nextRegs); err != nil {
			if !errors.Is(err, install.ErrROMNotInstalled) {
				slog.Warn("warm-boot reapply after reboot failed", "err", err)
			}
		} else {
			slog.Info("warm-boot reapplied after reboot (debug shortcut)")
		}
	}
}

func (e *emulator) togglePause() {
	e.paused.Store(!e.paused.Load())
	if e.paused.Load() {
		slog.Info("emulator paused")
	} else {
		slog.Info("emulator resumed")
	}
}

func (e *emulator) cleanup() {
	slog.Info("cleaning up emulator resources")
	e.paused.Store(true)
	close(e.stopChan)
	if e.ticker != nil {
		e.ticker.Stop()
	}
	// e.ula is nil for the SAM Coupé and ZX80/ZX81 (no Spectrum ULA), so guard
	// the close — otherwise quitting in those modes nil-panics.
	if e.ula != nil {
		e.ula.Close()
	}
	if e.samAudio != nil {
		_ = e.samAudio.Close()
	}
}

// getFormatName returns a human-readable name for the snapshot format
func getFormatName(format snapshot.SnapshotFormat) string {
	switch format {
	case snapshot.FormatSNA:
		return "SNA"
	case snapshot.FormatZ80:
		return "Z80"
	case snapshot.FormatSZX:
		return "SZX"
	default:
		return "Unknown"
	}
}

// fileSubmenu groups a run of menu items under a single labelled
// parent entry (a fyne submenu via ChildMenu). Used to tame the
// otherwise huge flat File menu into a handful of cohesive groups.
func fileSubmenu(label string, items ...*fyne.MenuItem) *fyne.MenuItem {
	mi := fyne.NewMenuItem(label, nil)
	mi.ChildMenu = fyne.NewMenu("", items...)
	return mi
}

// ensureFileExt appends ext to path (and renames the file on disk)
// when path doesn't already end in ext, case-insensitively. Used by
// the save dialogs so a user who types "shot" instead of "shot.png"
// still gets a correctly-named file. If the rename fails (e.g. the
// target already exists) the original path is returned unchanged.
func ensureFileExt(path, ext string) string {
	if strings.HasSuffix(strings.ToLower(path), strings.ToLower(ext)) {
		return path
	}
	withExt := path + ext
	// Don't clobber an unrelated existing file: os.Rename overwrites
	// silently on Unix, so if "shot.png" already exists and the user
	// typed "shot", keep the file at the name they actually chose
	// rather than destroying the other one.
	if _, err := os.Stat(withExt); err == nil {
		return path
	}
	if err := os.Rename(path, withExt); err != nil {
		return path
	}
	return withExt
}

// writeScreenshotPNG renders the current frame and writes it as a
// PNG to w. Works for every machine type: emu.ula.Render() returns
// the composited framebuffer — the classic ULA bitmap for 48K…+3,
// and the full Spectrum Next composite (ULA + Layer 2 + sprites +
// tilemap + LoRes through the active palette and SLU priority) for
// ModelNext, at whatever resolution the active Next video mode
// produces. The pixel data is copied before encode so the PNG write
// can't race the emulator goroutine mutating the framebuffer.
func writeScreenshotPNG(emu *emulator, w io.Writer) error {
	src := emu.renderFrame()
	imgCopy := image.NewRGBA(src.Bounds())
	copy(imgCopy.Pix, src.Pix)
	return png.Encode(w, imgCopy)
}

// flushSDWriteback persists guest SD-image writes to disk when the
// user opted in via --sd-writeback. The previous file is kept as
// .bak (see ImageSource.WriteBackTo). No-op when the flag is off,
// no image is mounted, or the guest never wrote.
func (e *emulator) flushSDWriteback() {
	if e.sdImageSrc == nil || e.sdImagePath == "" || !e.sdImageSrc.Dirty() {
		return
	}
	if err := e.sdImageSrc.WriteBackTo(e.sdImagePath); err != nil {
		slog.Error("sd-writeback failed", "path", e.sdImagePath, "err", err)
		return
	}
	slog.Info("sd-writeback complete", "path", e.sdImagePath, "backup", e.sdImagePath+".bak")
}

// applySnapshotToEmulator applies a loaded snapshot to the running emulator.
func applySnapshotToEmulator(emu *emulator, snap *snapshot.Snapshot) error {
	// Pause emulation during snapshot loading
	wasPaused := emu.paused.Load()
	if !emu.paused.Load() {
		emu.togglePause()
	}

	// Apply CPU state
	emu.cpu.A = snap.CPU.A
	emu.cpu.F = snap.CPU.F
	emu.cpu.B = snap.CPU.B
	emu.cpu.C = snap.CPU.C
	emu.cpu.D = snap.CPU.D
	emu.cpu.E = snap.CPU.E
	emu.cpu.H = snap.CPU.H
	emu.cpu.L = snap.CPU.L

	emu.cpu.A_ = snap.CPU.A_
	emu.cpu.F_ = snap.CPU.F_
	emu.cpu.B_ = snap.CPU.B_
	emu.cpu.C_ = snap.CPU.C_
	emu.cpu.D_ = snap.CPU.D_
	emu.cpu.E_ = snap.CPU.E_
	emu.cpu.H_ = snap.CPU.H_
	emu.cpu.L_ = snap.CPU.L_

	emu.cpu.IX = snap.CPU.IX
	emu.cpu.IY = snap.CPU.IY
	emu.cpu.SP = snap.CPU.SP
	emu.cpu.PC = snap.CPU.PC
	emu.cpu.I = snap.CPU.I
	emu.cpu.R = snap.CPU.R
	emu.cpu.IFF1 = snap.CPU.IFF1
	emu.cpu.Halted = snap.CPU.Halted
	emu.cpu.IFF2 = snap.CPU.IFF2
	emu.cpu.IM = snap.CPU.IM

	// Apply memory state
	for i := 0; i < 8; i++ {
		bank := emu.mem.GetPage(i)
		copy(bank, snap.Memory.RAM[i])
	}

	// Apply memory paging for 128K machines
	if snap.Memory.Is128K {
		emu.mem.PageMemory(snap.Memory.Port7FFD)
	}

	// Apply border color
	emu.ula.BorderColour = snap.CPU.BorderColor

	// Resume emulation if it was running before
	if !wasPaused {
		emu.togglePause()
	}

	return nil
}

// createSnapshotFromEmulator creates a snapshot from the current emulator state
func createSnapshotFromEmulator(emu *emulator) (*snapshot.Snapshot, error) {
	snap := snapshot.New()

	// Copy CPU state
	snap.CPU.A = emu.cpu.A
	snap.CPU.F = emu.cpu.F
	snap.CPU.B = emu.cpu.B
	snap.CPU.C = emu.cpu.C
	snap.CPU.D = emu.cpu.D
	snap.CPU.E = emu.cpu.E
	snap.CPU.H = emu.cpu.H
	snap.CPU.L = emu.cpu.L

	snap.CPU.A_ = emu.cpu.A_
	snap.CPU.F_ = emu.cpu.F_
	snap.CPU.B_ = emu.cpu.B_
	snap.CPU.C_ = emu.cpu.C_
	snap.CPU.D_ = emu.cpu.D_
	snap.CPU.E_ = emu.cpu.E_
	snap.CPU.H_ = emu.cpu.H_
	snap.CPU.L_ = emu.cpu.L_

	snap.CPU.IX = emu.cpu.IX
	snap.CPU.IY = emu.cpu.IY
	snap.CPU.SP = emu.cpu.SP
	snap.CPU.PC = emu.cpu.PC
	snap.CPU.I = emu.cpu.I
	snap.CPU.R = emu.cpu.R
	snap.CPU.IFF1 = emu.cpu.IFF1
	snap.CPU.Halted = emu.cpu.Halted
	snap.CPU.IFF2 = emu.cpu.IFF2
	snap.CPU.IM = emu.cpu.IM

	// Copy memory state
	for i := 0; i < 8; i++ {
		bank := emu.mem.GetPage(i)
		copy(snap.Memory.RAM[i], bank)
	}

	// Set memory configuration
	snap.Memory.Is128K = (emu.mem.GetCurrentModel() != roms.Model48K)
	if snap.Memory.Is128K {
		snap.Memory.Port7FFD = 0
	}

	// Copy border color
	snap.CPU.BorderColor = emu.ula.BorderColour

	return snap, nil
}

// withEmulationPaused runs fn with the emulation goroutine paused, then
// restores the previous pause state. Used by RZX start/stop helpers
// that mutate live CPU state and would otherwise race the emulation
// loop. Matches the pattern used by applySnapshotToEmulator.
func (e *emulator) withEmulationPaused(fn func() error) error {
	wasPaused := e.paused.Load()
	if !wasPaused {
		e.paused.Store(true)
	}
	err := fn()
	if !wasPaused {
		e.paused.Store(false)
	}
	return err
}

// startRZXPlayback installs the supplied RZX session as the active
// playback driver. Loads the embedded snapshot, wires the ULA's
// playback hook to the file's IN-byte stream, and switches the main
// loop into per-frame instruction-counted execution mode.
//
// Pauses the emulation goroutine for the duration of the state
// mutation so the CPU can't be mid-frame when the snapshot apply +
// T-state reset happens.
func (e *emulator) startRZXPlayback(file *rzx.File) error {
	if e.rzxRecord.Load() != nil {
		return fmt.Errorf("cannot start playback while recording")
	}
	return e.withEmulationPaused(func() error {
		pb := rzx.NewPlayback(file)
		snapBlock, err := pb.Start(0)
		if err != nil {
			return fmt.Errorf("rzx start playback: %w", err)
		}
		if snapBlock != nil {
			snap, err := rzx.DecodeSnapshot(snapBlock)
			if err != nil {
				return fmt.Errorf("rzx decode initial snapshot: %w", err)
			}
			if err := applySnapshotToEmulator(e, snap); err != nil {
				return fmt.Errorf("rzx apply initial snapshot: %w", err)
			}
		}
		e.cpu.SetTstates(uint64(pb.Tstates()))

		// Wire the ULA hook so every IN read pulls from the
		// recorded stream. Closure over pb avoids any pointer
		// chase on the per-IN hot path.
		e.ula.SetRZXPlaybackHook(func() (byte, bool) {
			b, err := pb.NextByte()
			if err != nil {
				return 0, false
			}
			return b, true
		})

		e.rzxPlayback.Store(pb)
		return nil
	})
}

// stopRZXPlayback tears down an active playback session. Idempotent.
// Safe to call from any goroutine — the atomic.Pointer clear plus the
// ULA hook clear are both lock-free.
func (e *emulator) stopRZXPlayback() {
	if e.rzxPlayback.Swap(nil) == nil {
		return
	}
	e.ula.SetRZXPlaybackHook(nil)
}

// startRZXRecording opens a new recording session that will be saved
// to filename when stopRZXRecording is called. The current emulator
// state is captured as the initial snapshot so playback starts from
// this point. Pauses the emulation goroutine during snapshot capture
// so the CPU state isn't sampled mid-frame.
func (e *emulator) startRZXRecording(filename string, competition bool) error {
	if e.rzxPlayback.Load() != nil {
		return fmt.Errorf("cannot start recording while playback is active")
	}
	return e.withEmulationPaused(func() error {
		rec := rzx.NewRecording()
		rec.AddCreator(&rzx.CreatorBlock{Program: "zx_go", Major: 1, Minor: 0})

		snap, err := createSnapshotFromEmulator(e)
		if err != nil {
			return fmt.Errorf("rzx capture snapshot: %w", err)
		}
		block, err := rzx.EncodeSnapshot(snap, rzx.SnapshotFormatSZX, false)
		if err != nil {
			return fmt.Errorf("rzx encode snapshot: %w", err)
		}
		rec.AddSnap(block, false)
		rec.AutosavesEnabled = !competition
		rec.CompetitionMode = competition
		rec.StartInput(uint32(e.cpu.Tstates()))

		e.ula.SetRZXRecordHook(rec.RecordIN)

		e.rzxRecord.Store(rec)
		e.rzxRecordFilename = filename
		return nil
	})
}

// stopRZXRecording finalises the in-progress recording (if any) and
// writes it out. The Recording is cleared even on write failure so the
// user can retry; pauses the emulation goroutine while sampling the
// final snapshot so the CPU state isn't read mid-frame.
func (e *emulator) stopRZXRecording() error {
	rec := e.rzxRecord.Swap(nil)
	if rec == nil {
		return nil
	}
	filename := e.rzxRecordFilename
	e.rzxRecordFilename = ""
	e.ula.SetRZXRecordHook(nil)

	// Embed a final snapshot so post-recording playback can resume
	// from the end (FUSE rzx.c:199, skipped in competition mode).
	if !rec.CompetitionMode {
		err := e.withEmulationPaused(func() error {
			snap, err := createSnapshotFromEmulator(e)
			if err != nil {
				return err
			}
			block, err := rzx.EncodeSnapshot(snap, rzx.SnapshotFormatSZX, false)
			if err != nil {
				return err
			}
			rec.AddSnap(block, false)
			return nil
		})
		if err != nil {
			slog.Error("RZX stop: final snapshot capture failed", "err", err)
		}
	}

	if err := rec.WriteFile(filename, rzx.WriteOptions{Compress: true}); err != nil {
		return fmt.Errorf("rzx write %s: %w", filename, err)
	}
	return nil
}

// makeMicrodriveMenu builds an 8-drive Microdrive submenu. Each child
// is itself a submenu with Insert / Save / Eject / Write Protect.
// All four actions delegate to peripherals.PeripheralManager helpers
// so the menu code stays a thin shell over the package boundary.
func makeMicrodriveMenu(emu *emulator, w fyne.Window) *fyne.MenuItem {
	root := fyne.NewMenuItem("Microdrives", nil)

	slotCount := emu.peripherals.MicrodriveSlotCount()
	driveItems := make([]*fyne.MenuItem, slotCount)
	for i := 0; i < slotCount; i++ {
		slot := i // capture
		insert := fyne.NewMenuItem("Insert Cartridge...", func() {
			fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				if reader == nil {
					return
				}
				path := reader.URI().Path()
				_ = reader.Close()
				if err := emu.peripherals.LoadMicrodrive(slot, path); err != nil {
					dialog.ShowError(fmt.Errorf("load microdrive: %w", err), w)
					return
				}
				dialog.ShowInformation("Microdrive", fmt.Sprintf("Cartridge loaded into Drive %d:\n%s", slot+1, filepath.Base(path)), w)
			}, w)
			fd.SetFilter(storage.NewExtensionFileFilter([]string{".mdr"}))
			fd.Show()
		})
		save := fyne.NewMenuItem("Save Cartridge...", func() {
			if !emu.peripherals.MicrodriveCartridgeInserted(slot) {
				dialog.ShowInformation("Microdrive", fmt.Sprintf("Drive %d has no cartridge.", slot+1), w)
				return
			}
			fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				if writer == nil {
					return
				}
				path := writer.URI().Path()
				_ = writer.Close()
				if !strings.HasSuffix(strings.ToLower(path), ".mdr") {
					path += ".mdr"
				}
				if err := emu.peripherals.SaveMicrodrive(slot, path); err != nil {
					dialog.ShowError(fmt.Errorf("save microdrive: %w", err), w)
					return
				}
				dialog.ShowInformation("Microdrive", fmt.Sprintf("Drive %d saved to:\n%s", slot+1, filepath.Base(path)), w)
			}, w)
			fd.SetFilter(storage.NewExtensionFileFilter([]string{".mdr"}))
			fd.Show()
		})
		eject := fyne.NewMenuItem("Eject", func() {
			emu.peripherals.EjectMicrodrive(slot)
		})
		wp := fyne.NewMenuItem("Toggle Write Protect", func() {
			emu.peripherals.SetMicrodriveWriteProtect(slot, !emu.peripherals.MicrodriveWriteProtected(slot))
		})

		driveItem := fyne.NewMenuItem(fmt.Sprintf("Drive %d", slot+1), nil)
		driveItem.ChildMenu = fyne.NewMenu("", insert, save, eject, wp)
		driveItems[i] = driveItem
	}
	root.ChildMenu = fyne.NewMenu("", driveItems...)
	return root
}

// enableInterface1 turns on the Interface 1 — pulls the IF1 ROM from
// the ROM manager (which loads it from roms/if1-2.rom or the embedded
// fallback), enables the IF1 in the peripheral manager, and installs
// the per-instruction page-in/page-out hooks on the Z80. Returns an
// error with a user-friendly message if the ROM is missing.
//
// State mutation runs under withEmulationPaused so the emulation
// goroutine isn't reading the CPU's hook fields while the UI thread
// is writing them — same race-avoidance pattern the snapshot loader
// uses.
func (e *emulator) enableInterface1() error {
	rom, ok := e.mem.GetROMManager().GetROM(roms.ROMINTERFACE1)
	if !ok {
		return fmt.Errorf("interface 1 ROM not found — drop if1-2.rom (8KB) into the roms/ directory; available from World of Spectrum and similar archives")
	}
	return e.withEmulationPaused(func() error {
		if err := e.peripherals.EnableInterface1(rom); err != nil {
			return err
		}
		dev := e.peripherals.IF1()
		e.cpu.PreFetchHook = dev.PreFetchHook
		e.cpu.PostFetchHook = dev.PostFetchHook
		return nil
	})
}

// disableInterface1 tears down the Interface 1: removes the Z80 page
// hooks and disables the device in the peripheral manager. Paired
// with enableInterface1's withEmulationPaused so the hook nil-out
// can't race with the emulation goroutine's per-instruction read.
func (e *emulator) disableInterface1() {
	_ = e.withEmulationPaused(func() error {
		e.cpu.PreFetchHook = nil
		e.cpu.PostFetchHook = nil
		e.peripherals.DisableInterface1()
		return nil
	})
}

// rzxRollbackToLastSnapshot truncates the in-progress recording back
// to the most recent snapshot block, restores that snapshot to the
// live emulator, and reopens the input recording window. Bound to the
// "RZX Rollback" menu item.
func (e *emulator) rzxRollbackToLastSnapshot() error {
	rec := e.rzxRecord.Load()
	if rec == nil {
		return fmt.Errorf("no recording in progress")
	}
	snapBlock, err := rec.Rollback()
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	snap, err := rzx.DecodeSnapshot(snapBlock)
	if err != nil {
		return fmt.Errorf("decode rollback snapshot: %w", err)
	}
	return e.withEmulationPaused(func() error {
		if err := applySnapshotToEmulator(e, snap); err != nil {
			return err
		}
		rec.StartInput(uint32(e.cpu.Tstates()))
		return nil
	})
}

// installTapeTrap installs a fast-load trap on the CPU that intercepts the
// 48K ROM LD-BYTES routine at 0x0556 and synthesises the load directly from
// the next tape block, avoiding the slow real-time pulse decoding.
//
// On entry to LD-BYTES (PC=0x0556) the contract is:
//
//	A          expected flag byte (header=0x00, data=0xFF)
//	F carry    set means LOAD, clear means VERIFY
//	IX         destination address
//	DE         number of bytes to load (excluding flag/checksum)
//
// Note: A/F (not A'/F') — the EX AF,AF' is the routine's *second*
// instruction, so it has not run yet at the trap point.
//
// The routine returns with carry set on success, carry clear on failure.
// We replicate this contract by reading bytes directly from the current
// tape block (which is stored as: flag byte, data bytes..., checksum byte).
// tapeTrapROMActive reports whether the 48 BASIC ROM (the one containing the
// LD-BYTES loader at $0556) is currently paged at $0000 for the active model.
// The fast-load trap may only fire then.
func tapeTrapROMActive(mem *memory.Memory) bool {
	switch mem.GetCurrentModel() {
	case roms.Model48K:
		return true // single ROM, always 48 BASIC
	case roms.Model128K, roms.ModelPlus2, roms.ModelPentagon:
		return mem.GetROMBank() == 1
	case roms.ModelPlus2A, roms.ModelPlus3:
		return mem.GetROMBank() == 3
	default:
		return false // Next / ZX8x have no classic LD-BYTES tape loader
	}
}

// tapeTrace, when ZX_GO_TAPE_TRACE is set, logs every LD-BYTES trap hit and
// decision to stderr — the diagnostic for tape loads that fail only in a real
// session (which the headless harness can't reproduce).
var tapeTrace = os.Getenv("ZX_GO_TAPE_TRACE") != ""

func installTapeTrap(emu *emulator) {
	emu.cpu.TrapCheck = func(pc uint16) bool {
		// The trap drives the Spectrum ULA tape player; machines without a
		// ULA (SAM Coupé, ZX80/81) share the $0556 PC but must never trap.
		if emu.ula == nil {
			return false
		}
		if pc != 0x0556 {
			return false
		}
		if tapeTrace {
			tp := emu.ula.GetTapePlayer()
			blk, more := -1, false
			if tp != nil {
				blk, more = tp.CurrentBlock(), tp.HasMoreBlocks()
			}
			fmt.Fprintf(os.Stderr, "[tapetrap] @0556 model=%s bank=%d active=%v tp=%v block=%d more=%v A=%02X carry=%v\n",
				roms.GetModelName(emu.mem.GetCurrentModel()), emu.mem.GetROMBank(),
				tapeTrapROMActive(emu.mem), tp != nil, blk, more, emu.cpu.A, emu.cpu.F&z80.FLAG_C != 0)
		}
		// Fire only when the 48 BASIC ROM — which holds LD-BYTES at $0556 — is
		// the ROM currently paged at $0000. That's always true on the 48K; on
		// the 128/+2/Pentagon it's ROM bank 1 (the 128 menu's "Tape Loader"
		// pages it in), and on the +2A/+3 it's ROM bank 3. On any other paged
		// ROM, $0556 is unrelated code and must not be trapped. (Previously the
		// trap was 48K-only, so 128K tape loading silently did nothing — the
		// per-frame tape player can't drive the edge-timed ROM loader.)
		if !tapeTrapROMActive(emu.mem) {
			return false
		}
		tp := emu.ula.GetTapePlayer()
		if tp == nil || !tp.HasMoreBlocks() {
			return false
		}

		block := tp.NextBlock()
		if block == nil {
			return false
		}

		expectedFlag := emu.cpu.A
		isLoad := (emu.cpu.F & z80.FLAG_C) != 0

		dst := emu.cpu.IX
		count := uint16(emu.cpu.D)<<8 | uint16(emu.cpu.E)

		success := true
		if len(block) < 1 {
			success = false
		} else if block[0] != expectedFlag {
			// Flag mismatch — emulate failure.
			if tapeTrace {
				fmt.Fprintf(os.Stderr, "[tapetrap] FLAG MISMATCH block %d flag=%02X expected=%02X -> FAIL (R Tape loading error)\n",
					emu.ula.GetTapePlayer().CurrentBlock()-1, block[0], expectedFlag)
			}
			success = false
		} else {
			// Block contains: flag, data..., checksum.
			data := block[1:]
			if len(data) > 0 {
				// Last byte of the block is the checksum.
				data = data[:len(data)-1]
			}
			n := int(count)
			if n > len(data) {
				if tapeTrace {
					fmt.Fprintf(os.Stderr, "[tapetrap] LENGTH MISMATCH want %d bytes but block %d has %d -> FAIL (R Tape loading error)\n",
						n, emu.ula.GetTapePlayer().CurrentBlock()-1, len(data))
				}
				n = len(data)
				success = false
			}
			if isLoad {
				for i := 0; i < n; i++ {
					emu.mem.Write(dst+uint16(i), data[i])
				}
			}
			// Advance IX and zero DE as the real routine would on success.
			emu.cpu.IX = dst + uint16(n)
			emu.cpu.D = 0
			emu.cpu.E = 0
		}

		// Update the carry flag in F (current accumulator's flags).
		if success {
			emu.cpu.F |= z80.FLAG_C
		} else {
			emu.cpu.F &^= z80.FLAG_C
		}

		// Return from LD-BYTES: pop the return address from the stack.
		// LD-BYTES is normally entered via CALL, so the stack holds the
		// caller's return address. We mimic the routine's RET by popping
		// directly into PC.
		low := emu.mem.Read(emu.cpu.SP)
		high := emu.mem.Read(emu.cpu.SP + 1)
		emu.cpu.SP += 2
		emu.cpu.PC = uint16(high)<<8 | uint16(low)

		slog.Debug("tape trap: loaded bytes", "count", count, "addr", fmt.Sprintf("$%04X", dst), "success", success)
		return true
	}
}

// parsePokes parses a multi-line poke string. Each non-empty, non-comment line
// must contain an address and a value, separated by whitespace, comma, or
// colon. Values are interpreted as hexadecimal. Returns a slice of (addr,val)
// pairs and an error describing the first malformed line.
func parsePokes(text string) ([]struct {
	Addr uint16
	Val  byte
}, error) {
	var result []struct {
		Addr uint16
		Val  byte
	}
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// Replace separators with spaces.
		line = strings.ReplaceAll(line, ",", " ")
		line = strings.ReplaceAll(line, ":", " ")
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: expected ADDR VALUE, got %q", i+1, raw)
		}
		// Strip optional 0x prefix.
		af := strings.TrimPrefix(strings.TrimPrefix(fields[0], "0x"), "0X")
		vf := strings.TrimPrefix(strings.TrimPrefix(fields[1], "0x"), "0X")
		addr, err := strconv.ParseUint(af, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid address %q: %w", i+1, fields[0], err)
		}
		val, err := strconv.ParseUint(vf, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid value %q: %w", i+1, fields[1], err)
		}
		result = append(result, struct {
			Addr uint16
			Val  byte
		}{Addr: uint16(addr), Val: byte(val)})
	}
	return result, nil
}

// aspectRatioLayout centres its single child at a fixed aspect ratio
// within the available space, adding black bars as needed.
type aspectRatioLayout struct {
	ratio float64 // width / height, e.g. 4.0/3.0
}

func (a *aspectRatioLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(320, 240)
}

func (a *aspectRatioLayout) Layout(objects []fyne.CanvasObject, containerSize fyne.Size) {
	if len(objects) == 0 {
		return
	}
	cw := float64(containerSize.Width)
	ch := float64(containerSize.Height)

	// Fit the aspect ratio inside the container
	var w, h float64
	if cw/ch > a.ratio {
		// Container is wider than 4:3 — pillarbox (black bars on sides)
		h = ch
		w = h * a.ratio
	} else {
		// Container is taller than 4:3 — letterbox (black bars top/bottom)
		w = cw
		h = w / a.ratio
	}

	x := (cw - w) / 2
	y := (ch - h) / 2

	objects[0].Move(fyne.NewPos(float32(x), float32(y)))
	objects[0].Resize(fyne.NewSize(float32(w), float32(h)))
}

// modelToConfigString maps a roms.SpectrumModel to its on-disk
// config representation. Stable strings so future renames in the
// roms package don't invalidate existing config files.
func modelToConfigString(m roms.SpectrumModel) string {
	switch m {
	case roms.Model128K:
		return "128K"
	case roms.ModelPlus2:
		return "+2"
	case roms.ModelPlus2A:
		return "+2A"
	case roms.ModelPlus3:
		return "+3"
	case roms.ModelNext:
		return "Next"
	case roms.ModelPentagon:
		return "Pentagon"
	case roms.ModelZX81:
		return "ZX81"
	case roms.ModelZX80:
		return "ZX80"
	}
	return "48K"
}

func joystickToConfigString(j JoystickType) string {
	switch j {
	case JoystickKempston:
		return "Kempston"
	case JoystickSinclair1:
		return "Sinclair1"
	case JoystickSinclair2:
		return "Sinclair2"
	case JoystickCursor:
		return "Cursor"
	}
	return "None"
}

func configStringToJoystick(s string) JoystickType {
	switch s {
	case "Kempston":
		return JoystickKempston
	case "Sinclair1":
		return JoystickSinclair1
	case "Sinclair2":
		return JoystickSinclair2
	case "Cursor":
		return JoystickCursor
	}
	return JoystickNone
}

// multifaceVariantToConfigString maps a multiface variant to its
// stable on-disk representation. Empty string means "no Multiface".
func multifaceVariantToConfigString(v multiface.MultifaceType) string {
	switch v {
	case multiface.Multiface1:
		return "MF1"
	case multiface.Multiface128:
		return "MF128"
	case multiface.Multiface3:
		return "MF3"
	}
	return ""
}

// configStringToMultifaceVariant inverts the above. An unrecognised
// or empty string returns Multiface128 (the package default) — callers
// must gate on cfg.Multiface != "" before calling.
func configStringToMultifaceVariant(s string) multiface.MultifaceType {
	switch s {
	case "MF1":
		return multiface.Multiface1
	case "MF3":
		return multiface.Multiface3
	}
	return multiface.Multiface128
}

// scaleToWindowSize maps a percentage (100/125/150/200/300) to the fyne window
// size. The returned height is padded by the menu-bar height so the emulator
// image keeps its intended scale rather than being squashed by the menu. Unknown
// values fall back to 200%.
func scaleToWindowSize(scale int) (float32, float32) {
	w, h := float32(640), float32(480) // 200% — the existing default
	switch scale {
	case 100:
		w, h = 320, 240
	case 125:
		w, h = 400, 300
	case 150:
		w, h = 480, 360
	case 300:
		w, h = 960, 720
	}
	return w, h + menuBarHeight()
}

// menuBarHeight returns the vertical space the window's main menu bar occupies.
// Derived from the theme so it stays correct across themes / DPI: Fyne sizes the
// bar's items as text height plus inner padding above and below.
func menuBarHeight() float32 {
	th := fyne.CurrentApp().Settings().Theme()
	return th.Size(theme.SizeNameText) + 2*th.Size(theme.SizeNameInnerPadding)
}

func main() {
	flags := parseCLI()
	zxlog.Setup(flags.logLevel)
	zxlog.Banner()

	// Capture-Next-snapshot mode: connect to a reference Next
	// emulator's ZRCP debug protocol, dump its full state, write
	// to install dir, exit. Skips emulator startup entirely.
	if flags.captureNextSnapshot != "" {
		if err := runCaptureNextSnapshot(flags.captureNextSnapshot); err != nil {
			fmt.Fprintf(os.Stderr, "capture-next-snapshot failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// First-divergence bisection mode (DEV): our Next boot vs a live
	// reference emulator, checkpointed at the n-th hit of an anchor PC.
	if flags.nextBisect != "" {
		if err := runNextBisectFromSpec(flags.nextBisect); err != nil {
			fmt.Fprintf(os.Stderr, "next-bisect failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Instruction-level PC-lockstep mode (DEV): step our Next and a live
	// reference emulator one instruction at a time from a matching checkpoint;
	// report the first instruction where the reference emulator doesn't follow our path.
	if flags.nextLockstep != "" {
		if err := runNextLockstepFromSpec(flags.nextLockstep); err != nil {
			fmt.Fprintf(os.Stderr, "next-lockstep failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// NextReg read-back diff mode (DEV): dump every NextReg ($00-$FF)
	// read-back from our Next and a live reference emulator at a checkpoint and
	// report which differ — finds read-back faithfulness gaps.
	if flags.nextNRDiff != "" {
		if err := runNextNRDiffFromSpec(flags.nextNRDiff); err != nil {
			fmt.Fprintf(os.Stderr, "next-nrdiff failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Logical-memory diff mode (DEV): compare our Next's full 64 KB logical
	// memory image to a live reference emulator at a checkpoint. Aimed at the
	// last register-matched hit — a memory diff there isolates the stale
	// cell forking the path; no diff redirects the hunt to port inputs.
	if flags.nextMemDiff != "" {
		if err := runNextMemDiffFromSpec(flags.nextMemDiff); err != nil {
			fmt.Fprintf(os.Stderr, "next-memdiff failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Headless mode skips the entire Fyne / GUI path. Wire trace
	// hooks against a freshly-constructed emulator, run frames,
	// optionally dump state, exit.
	if flags.headless {
		// The SAM has its own memory/IO and isn't wired into runHeadless's
		// Spectrum-bound instrumentation; use its dedicated headless path.
		if flags.startInSAM {
			runSAMHeadless(flags)
			return
		}
		runHeadless(flags)
		return
	}

	a := app.NewWithID("com.conorarmstrong.zxgo")
	a.SetIcon(spectrumIcon())

	// Load persisted settings. Missing or corrupt config falls back
	// to defaults so a fresh install (or a future-incompatible file)
	// still launches.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		slog.Warn("config load failed; using defaults", "err", cfgErr)
		cfg = &config.Config{}
	}
	// Hand off the SD-card paths to pkg/next/install so its
	// SDCardRoot / SDCardImage helpers find them. Done early
	// because setupNextSubsystems consults them.
	install.ConfiguredSDDir = cfg.NextSDDir
	install.ConfiguredSDImage = cfg.NextSDImage

	// Every launch starts in 48K — matches real Spectrum hardware
	// behaviour (cold boot is always 48K BASIC) and keeps Next mode
	// an opt-in per session, since it depends on user-installed
	// ROMs that may not be present on every machine. cfg.Model
	// is still persisted (the Machine menu writes it), but ignored
	// at startup. --next on the command line overrides this so
	// trace / debug runs can jump straight into Next mode.
	currentModel := roms.Model48K
	switch {
	case flags.startInNext:
		currentModel = roms.ModelNext
	case flags.startInZX81:
		currentModel = roms.ModelZX81
	case flags.startInZX80:
		currentModel = roms.ModelZX80
	case flags.startInPentagon:
		currentModel = roms.ModelPentagon
	case flags.startInSAM:
		currentModel = roms.ModelSAM
	}
	_ = cfg.Model
	currentScale := 200
	if cfg.Scale != 0 {
		currentScale = cfg.Scale
	}

	w := a.NewWindow(fmt.Sprintf("ZX Spectrum Emulator %s - %s", version.Version, roms.GetModelName(currentModel)))
	w.SetIcon(spectrumIcon())
	wScaleW, wScaleH := scaleToWindowSize(currentScale)
	w.Resize(fyne.NewSize(wScaleW, wScaleH))

	emu, err := newEmulator(currentModel)
	if err != nil {
		slog.Error("failed to create emulator", "err", err)
		os.Exit(1)
	}
	emu.window = w
	if flags.trdPath != "" {
		if err := emu.mountTRD(0, flags.trdPath); err != nil {
			slog.Error("failed to mount --trd image", "err", err)
			os.Exit(1)
		}
		slog.Info("mounted TR-DOS disk in drive A", "path", flags.trdPath)
	}
	// Wire trace hooks for the GUI emulator if any channels were
	// requested on the command line. closeFn flushes any open
	// trace-output file at process exit.
	_, closeTrace := installTraceHooks(emu, flags)
	defer closeTrace()

	// --trace-db in GUI mode: same ring recorder as headless so a
	// GUI-only crash flow can be captured and diffed against a
	// healthy headless boot.
	if flags.traceDB != "" {
		if tdb := newTraceDB(flags.traceDBKeep); tdb != nil {
			cpu := emu.cpu
			var dmcPager *divmmc.Pager
			if p, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok {
				dmcPager = p
			}
			emu.cpu.AddPreFetchHook("trace-db", func(pc uint16) {
				p7, p1, _ := emu.mem.GetPortState()
				bank := int((p7>>4)&1) | int((p1>>1)&2)
				alt := int(emu.mem.AltROMReg())
				dmc := 0
				if dmcPager != nil && dmcPager.IsPagedIn() {
					dmc = 1
				}
				tdb.record(traceDBRow{
					insn: cpu.InstructionCount(), pc: pc, sp: cpu.SP, bank: bank,
					alt: alt, dmc: dmc, frame: int(atomic.LoadInt32(&emu.frameCounter)),
					af: uint16(cpu.A)<<8 | uint16(cpu.F),
					bc: uint16(cpu.B)<<8 | uint16(cpu.C),
					de: uint16(cpu.D)<<8 | uint16(cpu.E),
					hl: uint16(cpu.H)<<8 | uint16(cpu.L),
					ix: cpu.IX, iy: cpu.IY,
				})
			})
			defer func() {
				if n, err := tdb.flushSQLite(flags.traceDB); err == nil {
					slog.Info("trace-db flushed", "path", flags.traceDB, "rows", n)
				}
			}()
			slog.Info("trace-db recording (GUI)", "path", flags.traceDB, "keep", flags.traceDBKeep)
		}
	}

	// Remote-debugger TCP server. Same wiring as headless — gated
	// by --debugger-port>0, the constructor returns nil when the
	// flag is off and WaitIfPaused is nil-safe. Installed before
	// the emulation goroutine starts so breakpoints set via
	// --debugger-pause-at-start fire on the very first M1 fetch.
	emu.rdbg = newRemoteDebugger(emu, flags.debuggerPort, flags.debuggerPauseAtStart, flags.debuggerHistory, flags.debuggerHistoryWide)
	if cfg.CRTFilter {
		emu.crtFilter.Store(true)
	}
	emu.joystickType = configStringToJoystick(cfg.Joystick)
	if emu.joystickType == JoystickKempston && emu.ula != nil {
		emu.ula.KempstonEnabled = true
	}
	if os.Getenv("ZX_GO_BORDER_TRACE") != "" && emu.ula != nil {
		var n int
		emu.ula.SetBorderTracer(func(port uint16, val byte, newBorder byte, scanline int) {
			if n < 200 {
				slog.Info("border-change",
					"port_full", fmt.Sprintf("$%04X", port),
					"port_lo", fmt.Sprintf("$%02X", byte(port)),
					"val", fmt.Sprintf("$%02X", val),
					"border", newBorder,
					"scanline", scanline,
					"pc", fmt.Sprintf("$%04X", emu.cpu.PC))
			}
			n++
		})
	}

	// saveConfig persists the current settings. Called from menu
	// handlers after they mutate state. Errors are logged but not
	// raised to the user — a failed persist shouldn't break the
	// emulation session.
	saveConfig := func() {
		cfg.Model = modelToConfigString(currentModel)
		cfg.Scale = currentScale
		cfg.Joystick = joystickToConfigString(emu.joystickType)
		cfg.CRTFilter = emu.crtFilter.Load()
		cfg.Disciple = emu.peripherals.IsDiscipleEnabled()
		cfg.Interface1 = emu.peripherals.IsInterface1Enabled()
		cfg.KempstonMouse = emu.peripherals.IsKempstonMouseEnabled()
		cfg.ZXPrinter = emu.peripherals.IsZXPrinterEnabled()
		cfg.SpecDrum = emu.speccyDAC != nil && emu.speccyDAC.SpecDrumEnabled()
		cfg.Covox = emu.speccyDAC != nil && emu.speccyDAC.CovoxEnabled()
		if emu.peripherals.IsMultifaceEnabled() {
			if mf := emu.peripherals.GetMultiface(); mf != nil {
				cfg.Multiface = multifaceVariantToConfigString(mf.GetVariant())
			}
		} else {
			cfg.Multiface = ""
		}
		if err := cfg.Save(); err != nil {
			slog.Warn("config save failed", "err", err)
		}
	}

	// Restore peripheral enable states. Best-effort — a missing
	// peripheral ROM (e.g. if1-2.rom) just logs and leaves the
	// peripheral disabled. DISCiPLE / Multiface / IF1 changes need
	// a cold-boot to install their hooks correctly, so reboot once
	// at the end if anything that affects boot was restored.
	peripheralNeedsReboot := false
	// Classic-bus peripherals (DISCiPLE, Multiface, IF1) do not
	// exist on Spectrum Next hardware and their port decodes clash
	// with the Next's I/O space — the DISCiPLE control port $1F
	// shadows the Kempston read the TBBLUE firmware polls during
	// boot, feeding it garbage and crashing the boot into a DI/HALT.
	// Skip restoring them when the current model is the Next.
	classicPeripheralsOK := currentModel != roms.ModelNext && !isZX8x(currentModel) && currentModel != roms.ModelSAM
	if cfg.Disciple && classicPeripheralsOK {
		if err := emu.peripherals.EnableDisciple("roms"); err != nil {
			slog.Warn("config: enable Disciple failed", "err", err)
		} else {
			dev := emu.peripherals.GetDisciple()
			emu.cpu.PreFetchHook = dev.PreFetchHook
			emu.cpu.PostFetchHook = dev.PostFetchHook
			peripheralNeedsReboot = true
		}
	}
	if cfg.Multiface != "" && classicPeripheralsOK {
		variant := configStringToMultifaceVariant(cfg.Multiface)
		if err := emu.peripherals.EnableMultiface(variant, "roms"); err != nil {
			slog.Warn("config: enable Multiface failed", "variant", cfg.Multiface, "err", err)
		} else {
			peripheralNeedsReboot = true
		}
	}
	if cfg.Interface1 {
		if err := emu.enableInterface1(); err != nil {
			slog.Warn("config: enable Interface 1 failed", "err", err)
		} else {
			peripheralNeedsReboot = true
		}
	}
	if cfg.KempstonMouse {
		emu.peripherals.EnableKempstonMouse()
	}
	if cfg.ZXPrinter {
		emu.peripherals.EnableZXPrinter()
	}
	if emu.speccyDAC != nil && classicPeripheralsOK {
		emu.speccyDAC.SetSpecDrum(cfg.SpecDrum)
		emu.speccyDAC.SetCovox(cfg.Covox)
	}
	if peripheralNeedsReboot {
		emu.reboot()
	}

	// Install fast tape loading trap (no-op until a tape is loaded).
	installTapeTrap(emu)

	// Use static image approach
	initialImage := emu.renderFrame()
	screen := canvas.NewImageFromImage(initialImage)
	screen.ScaleMode = canvas.ImageScalePixels

	// Create keyboard widget with event handlers. The mouse
	// callbacks route to the peripheral manager's Kempston mouse
	// — they're no-ops when the mouse isn't enabled, so the
	// handlers can always be installed.
	keyboardWidget := newKeyboardWidget(
		emu.handleKeyDown,
		emu.handleKeyUp,
	)
	keyboardWidget.onTypedRune = emu.handleTypedRune
	keyboardWidget.onFocusLost = emu.releaseAllInput
	keyboardWidget.onMouseMove = func(dx, dy int) {
		emu.peripherals.KempstonMouseMove(dx, dy)
	}
	keyboardWidget.onMouseBtn = func(btn int, pressed bool) {
		emu.peripherals.KempstonMouseButton(btn, pressed)
	}

	// Create model selection callback
	// var-declared (not :=) so the body can reference switchModel
	// itself — the ROM-download retry path re-invokes it on success.
	// rebuildEmulatorCore swaps the running emulator's machine core in place
	// (keeping the goroutines, channels and window). It is used when a model
	// switch crosses the ZX80/ZX81 boundary, because those machines have no ULA
	// or peripheral manager and so cannot use the in-place mem.SwitchModel path
	// that switches between Spectrum models.
	rebuildEmulatorCore := func(newModel roms.SpectrumModel) {
		wasPaused := emu.paused.Load()
		if !emu.paused.Load() {
			emu.togglePause()
		}
		if currentModel == roms.ModelNext {
			unwireNextSubsystems(emu)
		}
		fresh, err := newEmulator(newModel)
		if err != nil {
			dialog.ShowError(fmt.Errorf("failed to switch to %s: %w", roms.GetModelName(newModel), err), w)
			if !wasPaused {
				emu.togglePause()
			}
			return
		}
		// Stop the outgoing machine's audio devices before swapping the core,
		// so we don't leak the old SAM SAA player (or the old ULA's player when
		// crossing into a machine that has no ULA).
		if emu.samAudio != nil {
			_ = emu.samAudio.Close()
			emu.samAudio, emu.samAudioBuf = nil, nil
		}
		if emu.ula != nil && fresh.ula == nil {
			emu.ula.Close()
		}
		emu.cpu = fresh.cpu
		emu.mem = fresh.mem
		emu.ula = fresh.ula
		emu.kbd = fresh.kbd
		emu.peripherals = fresh.peripherals
		emu.zx8x = fresh.zx8x
		emu.sam = fresh.sam
		emu.samAudio = fresh.samAudio
		// fresh.speccyDAC is non-nil only for classic-Spectrum targets
		// (nil for ZX80/ZX81/SAM) — carry it over so the Peripherals
		// menu's SpecDrum/Covox items track the NEW core's DAC instead
		// of staying nil/stale from before the switch.
		emu.speccyDAC = fresh.speccyDAC
		// betaDisk is always nil on a freshly-built core (lazily created
		// by ensureBeta on first TR-DOS mount) — drop any stale interface
		// left over from before the switch. Without this, a Beta
		// interface mounted before crossing into ZX80/ZX81/SAM and back
		// would be "reused" by ensureBeta without ever being rewired to
		// the new mem/ula/cpu, so a later TRD mount would silently not
		// take effect.
		emu.betaDisk = fresh.betaDisk
		emu.model = newModel
		emu.nextEsxdos, emu.nextDAC, emu.nextRegs = fresh.nextEsxdos, fresh.nextDAC, fresh.nextRegs
		emu.nextPalette, emu.nextTilemap, emu.nextCopper = fresh.nextPalette, fresh.nextTilemap, fresh.nextCopper
		emu.nextSprites, emu.nextLayer2 = fresh.nextSprites, fresh.nextLayer2
		currentModel = newModel
		saveConfig()
		w.SetTitle(fmt.Sprintf("ZX Spectrum Emulator %s - %s", version.Version, roms.GetModelName(newModel)))
		if !wasPaused {
			emu.togglePause()
		}
		slog.Info("model switched (core rebuilt)", "model", roms.GetModelName(newModel))
		dialog.ShowInformation("Model Changed", fmt.Sprintf("Successfully switched to %s.", roms.GetModelName(newModel)), w)
	}

	var switchModel func(newModel roms.SpectrumModel)
	switchModel = func(newModel roms.SpectrumModel) {
		slog.Info("switching model", "to", roms.GetModelName(newModel))

		// Pre-flight for ModelNext: surface a friendly dialog when
		// the distro ROM isn't installed, rather than letting the
		// memory layer error out with a wrapped sentinel the user
		// has to decode. We do this BEFORE pausing or touching any
		// subsystem state so a "no, I'll install it first" path
		// leaves the running emulator completely undisturbed.
		if newModel == roms.ModelNext {
			if _, err := install.LoadROM(install.DistroROM); err != nil {
				slog.Warn("Spectrum Next switch: distro ROM not installed — offering download", "err", err)
				// The NextZXOS ROMs are licensed and NOT bundled with
				// the emulator. Offer to fetch them from the official
				// source; on success, retry the switch.
				offerNextROMDownload(w, func() { switchModel(newModel) })
				return
			}
		}

		// Crossing the ZX80/ZX81 or SAM Coupé boundary changes the machine type
		// entirely (own memory/IO, no Spectrum ULA), so rebuild the core instead
		// of the in-place Spectrum↔Spectrum paging swap below.
		if isZX8x(newModel) || emu.zx8x != nil || newModel == roms.ModelSAM || emu.sam != nil {
			rebuildEmulatorCore(newModel)
			return
		}

		// Pause emulation during switch
		wasPaused := emu.paused.Load()
		if !emu.paused.Load() {
			emu.togglePause()
		}

		// Tear down Next-only wiring BEFORE the memory swap when
		// leaving the Next: divMMC, esxDOS, Layer 2 etc all hold
		// pointers into the current memory map and need to be
		// detached before mem.SwitchModel reshuffles the bank
		// allocations.
		wasNext := currentModel == roms.ModelNext
		if wasNext && newModel != roms.ModelNext {
			unwireNextSubsystems(emu)
		}

		if err := emu.mem.SwitchModel(newModel); err != nil {
			slog.Error("failed to switch model", "err", err)
			dialog.ShowError(fmt.Errorf("failed to switch to %s: %w", roms.GetModelName(newModel), err), w)
			// Best-effort revert: if we detached Next subsystems
			// above, re-attach them so the user is back where they
			// started rather than in a half-wired state.
			if wasNext && newModel != roms.ModelNext {
				if rerr := wireNextSubsystems(emu); rerr != nil {
					slog.Error("failed to re-wire Next subsystems after switch failure", "err", rerr)
				}
			}
			if !wasPaused {
				emu.togglePause()
			}
			return
		}
		// emu.model must track the switch immediately: the emulation
		// goroutine's run loop reads it every frame (frameTStatesForModel)
		// to pick the per-model T-state budget, independently of
		// currentModel (a UI-only bookkeeping variable below).
		emu.model = newModel

		// Bring Next-only wiring up AFTER the memory swap when
		// entering the Next: the subsystems' divMMC ROM mapping,
		// Layer 2 framebuffer, and 8K MMU all expect the
		// ModelNext bank layout to already be in place.
		if newModel == roms.ModelNext && !wasNext {
			// Tear down edge-connector peripherals that clash with
			// the Next's I/O space first — a lingering DISCiPLE
			// (port $1F) crashes the firmware boot.
			disableClassicBusPeripherals(emu)
			if err := wireNextSubsystems(emu); err != nil {
				slog.Error("failed to wire Next subsystems", "err", err)
				dialog.ShowError(fmt.Errorf("failed to enable Next subsystems: %w", err), w)
				if !wasPaused {
					emu.togglePause()
				}
				return
			}
		}

		// Interface 2 cartridges are 48K-only. If the user switches
		// to a 128K-series model while a cartridge is inserted,
		// eject it — otherwise the cartridge ROM would shadow the
		// 128K ROM and break the new machine.
		if newModel != roms.Model48K && emu.peripherals.IsInterface2CartridgeInserted() {
			emu.peripherals.RemoveInterface2Cartridge()
		}

		currentModel = newModel
		saveConfig()

		// Automatic reboot after model switch
		emu.reboot()
		// Re-apply the per-model frame-INT timing (reboot's CPU reset keeps
		// the fields, but the model just changed).
		configureClassicIntTiming(emu.cpu, newModel)

		// Update window title to show current model
		w.SetTitle(fmt.Sprintf("ZX Spectrum Emulator %s - %s", version.Version, roms.GetModelName(currentModel)))

		// Resume emulation if it was running
		if !wasPaused {
			emu.togglePause()
		}

		slog.Info("model switched", "model", roms.GetModelName(currentModel))
		dialog.ShowInformation("Model Changed", fmt.Sprintf("Successfully switched to %s\n\nThe emulator has been automatically rebooted with the new ROM.", roms.GetModelName(currentModel)), w)
	}

	// ensureModelForSnapshot puts the machine on a stock 128K before applying
	// a plain 128K snapshot loaded from a user file, when the current model
	// can't run one as-is:
	//   - 48K has no bank paging at all, so the banks can't be placed;
	//   - +2A/+3 use a different paging scheme (port $1FFD) that a plain 128K
	//     snapshot doesn't carry, so it pages wrongly and crashes a few
	//     seconds in.
	// 128K/+2/Pentagon already page a 128K snapshot correctly, so they're left
	// alone. Reuses the fully-wired model switch (ROM reload + AY re-wire).
	// Only file loads need this; in-session restores (RZX/quick-save) already
	// match the running model.
	ensureModelForSnapshot := func(snap *snapshot.Snapshot) {
		if !snap.Memory.Is128K {
			return
		}
		switch emu.mem.GetCurrentModel() {
		case roms.Model48K, roms.ModelPlus2A, roms.ModelPlus3:
			switchModel(roms.Model128K)
		}
	}

	// loadFileByPath auto-detects file type by extension and routes
	// to the appropriate loader. Used by the unified "Open File..."
	// menu item, the Recent submenu, and the drag-and-drop window
	// handler. On success the path is prepended to cfg.RecentFiles
	// and a config save is triggered; the caller decides whether to
	// refresh the recent submenu UI.
	loadFileByPath := func(path string) (string, error) {
		ext := strings.ToLower(filepath.Ext(path))
		// On the ZX80/ZX81 (no ULA) only the native program formats are
		// loadable; Spectrum tape/disk/snapshot formats would dereference the
		// nil ula, so reject them with a clear message.
		if emu.zx8x != nil {
			switch ext {
			case ".p", ".81", ".o", ".80":
			default:
				return "", fmt.Errorf("%s files are Spectrum-only — switch to a Spectrum model first (the ZX80/ZX81 load .p/.o programs)", ext)
			}
		}
		switch ext {
		case ".tap":
			tp := ula.NewTapePlayer()
			if err := tp.LoadTAP(path); err != nil {
				return "", fmt.Errorf("load TAP: %w", err)
			}
			emu.ula.SetTapePlayer(tp)
			tp.Play()
			slog.Info("tape loaded", "format", "TAP", "blocks", tp.BlockCount(), "path", path)
			return "tape", nil
		case ".tzx":
			tp := ula.NewTapePlayer()
			if err := tp.LoadTZX(path); err != nil {
				return "", fmt.Errorf("load TZX: %w", err)
			}
			emu.ula.SetTapePlayer(tp)
			tp.Play()
			slog.Info("tape loaded", "format", "TZX", "blocks", tp.BlockCount(), "path", path)
			return "tape", nil
		case ".z80", ".sna", ".szx":
			snap := snapshot.New()
			if err := snap.Load(path); err != nil {
				return "", fmt.Errorf("load snapshot: %w", err)
			}
			ensureModelForSnapshot(snap)
			if err := applySnapshotToEmulator(emu, snap); err != nil {
				return "", fmt.Errorf("apply snapshot: %w", err)
			}
			slog.Info("snapshot loaded", "format", getFormatName(snap.Format), "is128k", snap.Memory.Is128K, "path", path)
			return "snapshot", nil
		case ".p", ".81":
			if emu.zx8x == nil || emu.model != roms.ModelZX81 {
				return "", fmt.Errorf("switch to the ZX81 first (Machine → Sinclair ZX81) before loading a .P program")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read .P: %w", err)
			}
			wasPaused := emu.paused.Load()
			if !wasPaused {
				emu.togglePause()
			}
			emu.zx8x.InjectP(data)
			if !wasPaused {
				emu.togglePause()
			}
			return "ZX81 program", nil
		case ".o", ".80":
			if emu.zx8x == nil || emu.model != roms.ModelZX80 {
				return "", fmt.Errorf("switch to the ZX80 first (Machine → Sinclair ZX80) before loading a .O program")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read .O: %w", err)
			}
			wasPaused := emu.paused.Load()
			if !wasPaused {
				emu.togglePause()
			}
			emu.zx8x.InjectO(data)
			if !wasPaused {
				emu.togglePause()
			}
			return "ZX80 program", nil
		case ".rzx":
			file, err := rzx.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("load RZX: %w", err)
			}
			if err := emu.startRZXPlayback(file); err != nil {
				return "", fmt.Errorf("start RZX playback: %w", err)
			}
			return "RZX recording", nil
		case ".nex":
			if currentModel != roms.ModelNext {
				return "", fmt.Errorf(".nex requires Spectrum Next mode (Machine → ZX Spectrum Next, then restart)")
			}
			if emu.sdImageSrc == nil {
				return "", fmt.Errorf(".nex loading needs a Spectrum Next SD card (none is configured)")
			}
			// Validate the file before offering to copy it.
			if _, err := nex.ParseFile(path); err != nil {
				return "", fmt.Errorf("parse NEX: %w", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read NEX: %w", err)
			}
			// .nex games are loaded through NextZXOS's own .nexload (so
			// games that depend on the OS run exactly as on hardware),
			// which requires the file on the SD card. Confirm the copy
			// with the user, then import and launch.
			emu.confirmImportNex(filepath.Base(path), data)
			return "Loading " + filepath.Base(path) + " via NextZXOS…", nil
		default:
			return "", fmt.Errorf("unrecognised file extension %q (supported: .tap .tzx .z80 .sna .szx .rzx; .nex requires Next mode)", ext)
		}
	}

	// recentSubmenu is rebuilt from cfg.RecentFiles after each
	// successful load. The menu items array is replaced wholesale
	// rather than mutated so Fyne re-lays out the menu on Refresh.
	recentSubmenu := fyne.NewMenuItem("Recent", nil)
	recentSubmenu.ChildMenu = fyne.NewMenu("")

	var refreshRecentMenu func()
	refreshRecentMenu = func() {
		if len(cfg.RecentFiles) == 0 {
			empty := fyne.NewMenuItem("(empty)", nil)
			empty.Disabled = true
			recentSubmenu.ChildMenu.Items = []*fyne.MenuItem{empty}
			return
		}
		// Recent files in MRU order, then a separator, then
		// "Clear Recent Files" so a corrupt/stale MRU isn't
		// trapped behind manual config.json editing.
		items := make([]*fyne.MenuItem, 0, len(cfg.RecentFiles)+2)
		for _, p := range cfg.RecentFiles {
			p := p // capture
			item := fyne.NewMenuItem(filepath.Base(p), func() {
				if _, err := loadFileByPath(p); err != nil {
					dialog.ShowError(err, w)
					return
				}
				cfg.AddRecent(p)
				saveConfig()
				refreshRecentMenu()
				fyne.Do(func() { w.MainMenu().Refresh() })
			})
			items = append(items, item)
		}
		items = append(items, fyne.NewMenuItemSeparator())
		items = append(items, fyne.NewMenuItem("Clear Recent Files", func() {
			cfg.RecentFiles = nil
			saveConfig()
			refreshRecentMenu()
			fyne.Do(func() { w.MainMenu().Refresh() })
		}))
		recentSubmenu.ChildMenu.Items = items
	}
	refreshRecentMenu()

	// Window-wide drag-and-drop: any URI dropped on the window
	// dispatches to loadFileByPath. Multiple files are loaded in
	// order; the last successful one wins (snapshots can replace
	// the running state, tapes replace each other, etc.).
	w.SetOnDropped(func(_ fyne.Position, items []fyne.URI) {
		for _, u := range items {
			path := u.Path()
			if _, err := loadFileByPath(path); err != nil {
				dialog.ShowError(err, w)
				continue
			}
			cfg.AddRecent(path)
		}
		saveConfig()
		refreshRecentMenu()
		fyne.Do(func() { w.MainMenu().Refresh() })
	})

	mainMenu := fyne.NewMainMenu(
		fyne.NewMenu("File",
			fyne.NewMenuItem("Quick Save State (F2)", func() {
				if err := emu.quickSaveState(); err != nil {
					dialog.ShowError(err, w)
				} else {
					dialog.ShowInformation("Quick Save", "State saved.\nPress F4 (or Quick Load State) to restore.", w)
				}
			}),
			fyne.NewMenuItem("Quick Load State (F4)", func() {
				if err := emu.quickLoadState(); err != nil {
					dialog.ShowError(err, w)
				}
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Open File...", func() {
				fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if reader == nil {
						return
					}
					path := reader.URI().Path()
					_ = reader.Close()
					kind, err := loadFileByPath(path)
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					cfg.AddRecent(path)
					saveConfig()
					refreshRecentMenu()
					fyne.Do(func() { w.MainMenu().Refresh() })
					dialog.ShowInformation("Opened", fmt.Sprintf("Loaded %s from:\n%s", kind, filepath.Base(path)), w)
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".tap", ".tzx", ".z80", ".sna", ".szx", ".rzx", ".nex", ".p", ".81", ".o", ".80"}))
				fd.Show()
			}),
			recentSubmenu,
			fyne.NewMenuItemSeparator(),
			fileSubmenu("Snapshots & ROM",
				fyne.NewMenuItem("Load ROM...", func() {
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						romPath := reader.URI().Path()
						_ = reader.Close()

						// Read the ROM file
						data, readErr := os.ReadFile(romPath)
						if readErr != nil {
							dialog.ShowError(fmt.Errorf("failed to read ROM: %w", readErr), w)
							return
						}

						// Validate size: 16KB for system ROMs, 8KB for peripheral ROMs
						if len(data) != 16384 && len(data) != 8192 {
							dialog.ShowError(fmt.Errorf("invalid ROM size: %d bytes (expected 16384 or 8192)", len(data)), w)
							return
						}

						// Pause, load the ROM into slot 0, reboot
						wasPaused := emu.paused.Load()
						if !emu.paused.Load() {
							emu.togglePause()
						}

						if len(data) == 16384 {
							// Replace the current model's primary ROM
							page := emu.mem.GetROMPage(0)
							if page != nil {
								copy(page, data)
							}
						}

						emu.reboot()

						if !wasPaused {
							emu.togglePause()
						}

						slog.Info("loaded ROM", "path", romPath, "size", len(data))
						dialog.ShowInformation("ROM Loaded", fmt.Sprintf("Loaded %s\n(%d bytes)\n\nEmulator rebooted.", reader.URI().Name(), len(data)), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".rom"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Load Snapshot...", func() {
					slog.Debug("load snapshot dialog opened")
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						slog.Info("snapshot selected", "path", reader.URI().Path())

						// Load the snapshot
						snap := snapshot.New()
						if err := snap.Load(reader.URI().Path()); err != nil {
							dialog.ShowError(fmt.Errorf("failed to load snapshot: %w", err), w)
							_ = reader.Close()
							return
						}

						// Bring the machine up to 128K first if this is a 128K
						// snapshot (a 48K machine can't page its banks).
						ensureModelForSnapshot(snap)

						// Apply snapshot to emulator
						if err := applySnapshotToEmulator(emu, snap); err != nil {
							dialog.ShowError(fmt.Errorf("failed to apply snapshot: %w", err), w)
							_ = reader.Close()
							return
						}

						slog.Info("snapshot loaded", "format", getFormatName(snap.Format))
						dialog.ShowInformation("Snapshot Loaded", fmt.Sprintf("Successfully loaded %s snapshot from:\n%s", getFormatName(snap.Format), reader.URI().Name()), w)
						_ = reader.Close()
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".z80", ".sna", ".szx"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Save Snapshot...", func() {
					slog.Debug("save snapshot dialog opened")
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						slog.Info("snapshot save location selected", "path", writer.URI().Path())

						// Create snapshot from current emulator state
						snap, err := createSnapshotFromEmulator(emu)
						if err != nil {
							dialog.ShowError(fmt.Errorf("failed to create snapshot: %w", err), w)
							_ = writer.Close()
							return
						}

						// Save the snapshot
						if err := snap.Save(writer.URI().Path()); err != nil {
							dialog.ShowError(fmt.Errorf("failed to save snapshot: %w", err), w)
							_ = writer.Close()
							return
						}

						slog.Info("snapshot saved", "format", getFormatName(snap.Format))
						dialog.ShowInformation("Snapshot Saved", fmt.Sprintf("Successfully saved %s snapshot to:\n%s", getFormatName(snap.Format), writer.URI().Name()), w)
						_ = writer.Close()
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".z80", ".sna", ".szx"}))
					fd.Show()
				}),
			),
			fileSubmenu("Spectrum Next",
				fyne.NewMenuItem("Install Next ROMs...", func() { installNextROM(w) }),
				fyne.NewMenuItem("Set Next SD Card Directory...", func() {
					fd := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if uri == nil {
							return
						}
						dir := uri.Path()
						cfg.NextSDDir = dir
						cfg.NextSDImage = "" // dir takes precedence — clear image
						install.ConfiguredSDDir = dir
						install.ConfiguredSDImage = ""
						if err := cfg.Save(); err != nil {
							slog.Warn("config save failed", "err", err)
						}
						dialog.ShowInformation("Next SD Card",
							"SD card directory set to:\n"+dir+
								"\n\nRestart ModelNext (Machine menu) to pick up the change.",
							w)
					}, w)
					fd.Show()
				}),
				fyne.NewMenuItem("Set Next SD Card Image (.img/.mmc)...", func() {
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						path := reader.URI().Path()
						_ = reader.Close()
						cfg.NextSDImage = path
						cfg.NextSDDir = "" // image takes precedence — clear dir
						install.ConfiguredSDImage = path
						install.ConfiguredSDDir = ""
						if err := cfg.Save(); err != nil {
							slog.Warn("config save failed", "err", err)
						}
						dialog.ShowInformation("Next SD Card",
							"SD card image set to:\n"+path+
								"\n\nRestart ModelNext (Machine menu) to pick up the change.",
							w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".img", ".mmc", ".IMG", ".MMC"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Clear Next SD Card Setting", func() {
					cfg.NextSDDir = ""
					cfg.NextSDImage = ""
					install.ConfiguredSDDir = ""
					install.ConfiguredSDImage = ""
					if err := cfg.Save(); err != nil {
						slog.Warn("config save failed", "err", err)
					}
					dialog.ShowInformation("Next SD Card",
						"SD card setting cleared. The emulator will fall back to "+
							"its default search (roms/next/sd if present).", w)
				}),
			),
			fileSubmenu("Tapes, Disks & Cartridges",
				fyne.NewMenuItem("Load Tape (TAP)...", func() {
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						tp := ula.NewTapePlayer()
						if err := tp.LoadTAP(reader.URI().Path()); err != nil {
							dialog.ShowError(fmt.Errorf("failed to load TAP: %w", err), w)
							_ = reader.Close()
							return
						}
						emu.ula.SetTapePlayer(tp)
						tp.Play()
						slog.Info("tape loaded", "format", "TAP", "blocks", tp.BlockCount(), "path", reader.URI().Path())
						dialog.ShowInformation("Tape Loaded", fmt.Sprintf("Loaded %d blocks from:\n%s\n\nTape is now playing.", tp.BlockCount(), reader.URI().Name()), w)
						_ = reader.Close()
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".tap"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Load Tape (TZX)...", func() {
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						tp := ula.NewTapePlayer()
						if err := tp.LoadTZX(reader.URI().Path()); err != nil {
							dialog.ShowError(fmt.Errorf("failed to load TZX: %w", err), w)
							_ = reader.Close()
							return
						}
						emu.ula.SetTapePlayer(tp)
						tp.Play()
						slog.Info("tape loaded", "format", "TZX", "blocks", tp.BlockCount(), "path", reader.URI().Path())
						dialog.ShowInformation("Tape Loaded", fmt.Sprintf("Loaded %d blocks from:\n%s\n\nTape is now playing.", tp.BlockCount(), reader.URI().Name()), w)
						_ = reader.Close()
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".tzx"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Insert Interface 2 Cartridge...", func() {
					insertInterface2Cartridge(emu, w, currentModel)
				}),
				fyne.NewMenuItem("Eject Interface 2 Cartridge", func() {
					ejectInterface2Cartridge(emu, w)
				}),
				fyne.NewMenuItem("Load DISCiPLE Disk 1...", func() {
					loadDiscipleDisk(emu, w, 0)
				}),
				fyne.NewMenuItem("Load DISCiPLE Disk 2...", func() {
					loadDiscipleDisk(emu, w, 1)
				}),
				fyne.NewMenuItem("Load Disk A...", func() {
					loadPlus3Disk(emu, w, currentModel, 0)
				}),
				fyne.NewMenuItem("Load Disk B...", func() {
					loadPlus3Disk(emu, w, currentModel, 1)
				}),
				fyne.NewMenuItem("Load TR-DOS Disk A (.TRD)...", func() {
					loadTRDDisk(emu, w, 0)
				}),
				fyne.NewMenuItem("Load TR-DOS Disk B (.TRD)...", func() {
					loadTRDDisk(emu, w, 1)
				}),
				fyne.NewMenuItem("Eject TR-DOS Disk A", func() {
					emu.ejectTRD(0)
				}),
				fyne.NewMenuItem("Eject TR-DOS Disk B", func() {
					emu.ejectTRD(1)
				}),
				fyne.NewMenuItem("Load SAM Disk 1 (.mgt/.sad)...", func() {
					loadSAMDisk(emu, w, 0)
				}),
				fyne.NewMenuItem("Load SAM Disk 2 (.mgt/.sad)...", func() {
					loadSAMDisk(emu, w, 1)
				}),
				fyne.NewMenuItem("Save Disk A (DSK)...", func() {
					savePlus3Disk(emu, w, 0)
				}),
				fyne.NewMenuItem("Save Disk B (DSK)...", func() {
					savePlus3Disk(emu, w, 1)
				}),
				fyne.NewMenuItem("Eject Disk A", func() {
					emu.peripherals.EjectPlus3Disk(0)
				}),
				fyne.NewMenuItem("Eject Disk B", func() {
					emu.peripherals.EjectPlus3Disk(1)
				}),
				func() *fyne.MenuItem {
					wpA := false
					item := fyne.NewMenuItem("Write Protect Disk A", nil)
					item.Action = func() {
						wpA = !wpA
						emu.peripherals.SetPlus3WriteProtect(0, wpA)
						if wpA {
							item.Label = "Unprotect Disk A"
						} else {
							item.Label = "Write Protect Disk A"
						}
						fyne.Do(func() { w.MainMenu().Refresh() })
					}
					return item
				}(),
				func() *fyne.MenuItem {
					wpB := false
					item := fyne.NewMenuItem("Write Protect Disk B", nil)
					item.Action = func() {
						wpB = !wpB
						emu.peripherals.SetPlus3WriteProtect(1, wpB)
						if wpB {
							item.Label = "Unprotect Disk B"
						} else {
							item.Label = "Write Protect Disk B"
						}
						fyne.Do(func() { w.MainMenu().Refresh() })
					}
					return item
				}(),
				func() *fyne.MenuItem {
					speedlockOn := false
					item := fyne.NewMenuItem("Enable Speedlock Workaround", nil)
					item.Action = func() {
						speedlockOn = !speedlockOn
						emu.peripherals.SetPlus3Speedlock(speedlockOn)
						if speedlockOn {
							item.Label = "Disable Speedlock Workaround"
						} else {
							item.Label = "Enable Speedlock Workaround"
						}
						fyne.Do(func() { w.MainMenu().Refresh() })
					}
					return item
				}(),
			),
			fileSubmenu("Recording (RZX)",
				fyne.NewMenuItem("Open RZX Recording...", func() {
					fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if reader == nil {
							return
						}
						path := reader.URI().Path()
						_ = reader.Close()
						file, err := rzx.ReadFile(path)
						if err != nil {
							dialog.ShowError(fmt.Errorf("failed to load RZX: %w", err), w)
							return
						}
						if err := emu.startRZXPlayback(file); err != nil {
							dialog.ShowError(fmt.Errorf("failed to start RZX playback: %w", err), w)
							return
						}
						dialog.ShowInformation("RZX Playback", fmt.Sprintf("Playing back:\n%s", filepath.Base(path)), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".rzx"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Stop RZX Playback", func() {
					emu.stopRZXPlayback()
				}),
				fyne.NewMenuItem("Start RZX Recording...", func() {
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						path := writer.URI().Path()
						_ = writer.Close()
						if !strings.HasSuffix(strings.ToLower(path), ".rzx") {
							path += ".rzx"
						}
						if err := emu.startRZXRecording(path, false); err != nil {
							dialog.ShowError(fmt.Errorf("start RZX recording: %w", err), w)
							return
						}
						dialog.ShowInformation("RZX Recording", fmt.Sprintf("Recording to:\n%s", filepath.Base(path)), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".rzx"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Stop RZX Recording", func() {
					if err := emu.stopRZXRecording(); err != nil {
						dialog.ShowError(fmt.Errorf("stop RZX recording: %w", err), w)
						return
					}
					dialog.ShowInformation("RZX Recording", "Recording stopped and saved.", w)
				}),
				fyne.NewMenuItem("RZX Rollback (last snapshot)", func() {
					if err := emu.rzxRollbackToLastSnapshot(); err != nil {
						dialog.ShowError(fmt.Errorf("RZX rollback: %w", err), w)
					}
				}),
			),
			makeMicrodriveMenu(emu, w),
			fileSubmenu("ZX Printer",
				fyne.NewMenuItem("Save ZX Printer Output (PNG)...", func() {
					if !emu.peripherals.IsZXPrinterEnabled() {
						dialog.ShowInformation("ZX Printer", "ZX Printer is not enabled.\nEnable it from the Peripherals menu first.", w)
						return
					}
					printer := emu.peripherals.ZXPrinter()
					if printer.Rows() == 0 {
						dialog.ShowInformation("ZX Printer", "Nothing has been printed yet.", w)
						return
					}
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						path := writer.URI().Path()
						_ = writer.Close()
						if !strings.HasSuffix(strings.ToLower(path), ".png") {
							path += ".png"
						}
						if err := printer.Save(path); err != nil {
							dialog.ShowError(fmt.Errorf("save printer output: %w", err), w)
							return
						}
						dialog.ShowInformation("ZX Printer", fmt.Sprintf("Saved %d rows to:\n%s", printer.Rows(), filepath.Base(path)), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".png"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Clear ZX Printer Output", func() {
					if !emu.peripherals.IsZXPrinterEnabled() {
						return
					}
					emu.peripherals.ZXPrinter().Clear()
				}),
			),
			fileSubmenu("Save Tape",
				fyne.NewMenuItem("Save Tape (TAP)...", func() {
					tp := emu.ula.GetTapePlayer()
					if tp == nil || tp.BlockCount() == 0 {
						dialog.ShowInformation("Save Tape", "No tape loaded.", w)
						return
					}
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						path := writer.URI().Path()
						_ = writer.Close()
						if err := tp.SaveTAP(path); err != nil {
							dialog.ShowError(fmt.Errorf("failed to save TAP: %w", err), w)
							return
						}
						dialog.ShowInformation("Tape Saved", fmt.Sprintf("Saved %d block(s) to:\n%s", tp.BlockCount(), writer.URI().Name()), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".tap"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Save Tape (TZX)...", func() {
					tp := emu.ula.GetTapePlayer()
					if tp == nil || tp.BlockCount() == 0 {
						dialog.ShowInformation("Save Tape", "No tape loaded.", w)
						return
					}
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						path := writer.URI().Path()
						_ = writer.Close()
						if err := tp.SaveTZX(path); err != nil {
							dialog.ShowError(fmt.Errorf("failed to save TZX: %w", err), w)
							return
						}
						dialog.ShowInformation("Tape Saved", fmt.Sprintf("Saved %d block(s) to:\n%s", tp.BlockCount(), writer.URI().Name()), w)
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".tzx"}))
					fd.Show()
				}),
				fyne.NewMenuItem("Stop Tape", func() {
					if emu.ula != nil {
						emu.ula.SetTapePlayer(nil)
						emu.ula.TapeIn = false
					}
				}),
				func() *fyne.MenuItem {
					item := fyne.NewMenuItem("Fast Tape Loading", nil)
					item.Checked = emu.fastTape.Load()
					item.Action = func() {
						emu.fastTape.Store(!emu.fastTape.Load())
						item.Checked = emu.fastTape.Load()
						fyne.Do(func() { w.MainMenu().Refresh() })
					}
					return item
				}(),
				fyne.NewMenuItem("Tape Browser...", func() {
					tp := emu.ula.GetTapePlayer()
					if tp == nil {
						dialog.ShowInformation("Tape Browser", "No tape loaded.", w)
						return
					}
					blocks := tp.Blocks()
					if len(blocks) == 0 {
						dialog.ShowInformation("Tape Browser", "Tape contains no blocks.", w)
						return
					}
					current := tp.CurrentBlock()
					items := make([]string, len(blocks))
					for i, b := range blocks {
						marker := "  "
						if i == current {
							marker = "▶ "
						}
						if b.Title != "" {
							items[i] = fmt.Sprintf("%s%3d  %-10s  %5d B  %q", marker, b.Index, b.Type, b.Length, b.Title)
						} else {
							items[i] = fmt.Sprintf("%s%3d  %-10s  %5d B  flag=0x%02X", marker, b.Index, b.Type, b.Length, b.FlagByte)
						}
					}
					list := widget.NewList(
						func() int { return len(items) },
						func() fyne.CanvasObject { return widget.NewLabel("") },
						func(id widget.ListItemID, obj fyne.CanvasObject) {
							obj.(*widget.Label).SetText(items[id])
						},
					)
					selected := current
					list.OnSelected = func(id widget.ListItemID) { selected = id }
					list.Select(current)

					content := container.NewBorder(
						widget.NewLabel(fmt.Sprintf("%d blocks  •  current: %d", len(blocks), current)),
						nil, nil, nil,
						list,
					)
					d := dialog.NewCustomConfirm(
						"Tape Browser",
						"Jump to selected",
						"Close",
						content,
						func(ok bool) {
							if !ok {
								return
							}
							tp.SeekToBlock(selected)
							tp.Play()
						},
						w,
					)
					d.Resize(fyne.NewSize(520, 400))
					d.Show()
				}),
			),
			fyne.NewMenuItemSeparator(),
			func() *fyne.MenuItem {
				item := fyne.NewMenuItem("Start Recording (WAV)...", nil)
				item.Action = func() {
					if emu.ula.IsRecording() {
						if err := emu.ula.StopRecording(); err != nil {
							dialog.ShowError(fmt.Errorf("failed to stop recording: %w", err), w)
							return
						}
						item.Label = "Start Recording (WAV)..."
						fyne.Do(func() { w.MainMenu().Refresh() })
						dialog.ShowInformation("Recording", "Recording stopped.", w)
						return
					}
					fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						if writer == nil {
							return
						}
						path := writer.URI().Path()
						// We only need the path; close the writer and open
						// our own file inside the audio package.
						_ = writer.Close()
						if err := emu.ula.StartRecording(path); err != nil {
							dialog.ShowError(fmt.Errorf("failed to start recording: %w", err), w)
							return
						}
						item.Label = "Stop Recording"
						fyne.Do(func() { w.MainMenu().Refresh() })
					}, w)
					fd.SetFilter(storage.NewExtensionFileFilter([]string{".wav"}))
					fd.Show()
				}
				return item
			}(),
			fyne.NewMenuItem("Save Screenshot...", func() {
				fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if writer == nil {
						return
					}
					path := writer.URI().Path()
					werr := writeScreenshotPNG(emu, writer)
					_ = writer.Close()
					if werr != nil {
						dialog.ShowError(fmt.Errorf("failed to write PNG: %w", werr), w)
						return
					}
					// Auto-add the .png extension when the user didn't
					// type one — fyne saves to the exact name given, so
					// rename the just-written file.
					path = ensureFileExt(path, ".png")
					dialog.ShowInformation("Screenshot Saved", "Saved screenshot to:\n"+filepath.Base(path), w)
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".png"}))
				fd.Show()
			}),
		),
		fyne.NewMenu("Machine",
			fyne.NewMenuItem("48K", func() { switchModel(roms.Model48K) }),
			fyne.NewMenuItem("128K", func() { switchModel(roms.Model128K) }),
			fyne.NewMenuItem("+2", func() { switchModel(roms.ModelPlus2) }),
			fyne.NewMenuItem("+2A", func() { switchModel(roms.ModelPlus2A) }),
			fyne.NewMenuItem("+3", func() { switchModel(roms.ModelPlus3) }),
			fyne.NewMenuItem("Pentagon 128", func() { switchModel(roms.ModelPentagon) }),
			fyne.NewMenuItem("SAM Coupé", func() { switchModel(roms.ModelSAM) }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Sinclair ZX81", func() { switchModel(roms.ModelZX81) }),
			fyne.NewMenuItem("Sinclair ZX80", func() { switchModel(roms.ModelZX80) }),
			fyne.NewMenuItemSeparator(),
			nextMenuItem(switchModel),
			cpuSpeedMenuItem(emu),
		),
		fyne.NewMenu("View",
			fyne.NewMenuItem("100% (320x240)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(scaleToWindowSize(100)))
				currentScale = 100
				saveConfig()
			}),
			fyne.NewMenuItem("125% (400x300)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(scaleToWindowSize(125)))
				currentScale = 125
				saveConfig()
			}),
			fyne.NewMenuItem("150% (480x360)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(scaleToWindowSize(150)))
				currentScale = 150
				saveConfig()
			}),
			fyne.NewMenuItem("200% (640x480)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(scaleToWindowSize(200)))
				currentScale = 200
				saveConfig()
			}),
			fyne.NewMenuItem("300% (960x720)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(scaleToWindowSize(300)))
				currentScale = 300
				saveConfig()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Full Screen", func() {
				w.SetFullScreen(true)
			}),
			func() *fyne.MenuItem {
				initialLabel := "Enable CRT Filter"
				if emu.crtFilter.Load() {
					initialLabel = "Disable CRT Filter"
				}
				item := fyne.NewMenuItem(initialLabel, nil)
				item.Action = func() {
					on := !emu.crtFilter.Load()
					emu.crtFilter.Store(on)
					if on {
						item.Label = "Disable CRT Filter"
					} else {
						item.Label = "Enable CRT Filter"
					}
					saveConfig()
					fyne.Do(func() { w.MainMenu().Refresh() })
				}
				return item
			}(),
		),
		func() *fyne.Menu {
			discipleItem := fyne.NewMenuItem("Enable Disciple", nil)
			mf1Item := fyne.NewMenuItem("Enable Multiface 1", nil)
			mf128Item := fyne.NewMenuItem("Enable Multiface 128", nil)
			mf3Item := fyne.NewMenuItem("Enable Multiface 3", nil)
			if1Item := fyne.NewMenuItem("Enable Interface 1", nil)
			joyNoneItem := fyne.NewMenuItem("Joystick: None", nil)
			joyKempstonItem := fyne.NewMenuItem("Joystick: Kempston", nil)
			joySinclair1Item := fyne.NewMenuItem("Joystick: Sinclair (left, 1-5)", nil)
			joySinclair2Item := fyne.NewMenuItem("Joystick: Sinclair (right, 6-0)", nil)
			joyCursorItem := fyne.NewMenuItem("Joystick: Cursor / Protek", nil)
			kempMouseItem := fyne.NewMenuItem("Enable Kempston Mouse", nil)
			zxPrinterItem := fyne.NewMenuItem("Enable ZX Printer", nil)
			specDrumItem := fyne.NewMenuItem("Enable SpecDrum", nil)
			covoxItem := fyne.NewMenuItem("Enable Covox", nil)

			updateLabels := func() {
				if emu.peripherals.IsDiscipleEnabled() {
					discipleItem.Label = "Disable Disciple"
				} else {
					discipleItem.Label = "Enable Disciple"
				}
				if emu.peripherals.IsMultifaceEnabled() {
					mf1Item.Label = "Disable Multiface"
					mf128Item.Disabled = true
					mf3Item.Disabled = true
				} else {
					mf1Item.Label = "Enable Multiface 1"
					mf128Item.Label = "Enable Multiface 128"
					mf3Item.Label = "Enable Multiface 3"
					mf128Item.Disabled = false
					mf3Item.Disabled = false
				}
				if emu.peripherals.IsInterface1Enabled() {
					if1Item.Label = "Disable Interface 1"
				} else {
					if1Item.Label = "Enable Interface 1"
				}
				// IF1 is 48K-only — grey the menu out on other models.
				if1Item.Disabled = !emu.peripherals.CanEnableInterface1() && !emu.peripherals.IsInterface1Enabled()
				// Show a check next to the active joystick by re-labelling.
				marker := func(t JoystickType, base string) string {
					if emu.joystickType == t {
						return "✓ " + base
					}
					return "  " + base
				}
				joyNoneItem.Label = marker(JoystickNone, "Joystick: None")
				joyKempstonItem.Label = marker(JoystickKempston, "Joystick: Kempston")
				joySinclair1Item.Label = marker(JoystickSinclair1, "Joystick: Sinclair (left, 1-5)")
				joySinclair2Item.Label = marker(JoystickSinclair2, "Joystick: Sinclair (right, 6-0)")
				joyCursorItem.Label = marker(JoystickCursor, "Joystick: Cursor / Protek")
				if emu.peripherals.IsKempstonMouseEnabled() {
					kempMouseItem.Label = "Disable Kempston Mouse"
				} else {
					kempMouseItem.Label = "Enable Kempston Mouse"
				}
				if emu.peripherals.IsZXPrinterEnabled() {
					zxPrinterItem.Label = "Disable ZX Printer"
				} else {
					zxPrinterItem.Label = "Enable ZX Printer"
				}
				// SpecDrum / Covox are classic-Spectrum DACs (nil on Next/ZX8x).
				specDrumItem.Disabled = emu.speccyDAC == nil
				covoxItem.Disabled = emu.speccyDAC == nil
				if emu.speccyDAC != nil && emu.speccyDAC.SpecDrumEnabled() {
					specDrumItem.Label = "Disable SpecDrum"
				} else {
					specDrumItem.Label = "Enable SpecDrum"
				}
				if emu.speccyDAC != nil && emu.speccyDAC.CovoxEnabled() {
					covoxItem.Label = "Disable Covox"
				} else {
					covoxItem.Label = "Enable Covox"
				}
			}

			discipleItem.Action = func() {
				if emu.peripherals.IsDiscipleEnabled() {
					emu.cpu.PreFetchHook = nil
					emu.cpu.PostFetchHook = nil
					emu.peripherals.DisableDisciple()
					emu.reboot()
				} else {
					if err := emu.peripherals.EnableDisciple("roms"); err != nil {
						dialog.ShowError(fmt.Errorf("failed to enable Disciple: %w", err), w)
					} else {
						dev := emu.peripherals.GetDisciple()
						emu.cpu.PreFetchHook = dev.PreFetchHook
						emu.cpu.PostFetchHook = dev.PostFetchHook
						emu.reboot() // cold boot GDOS
					}
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			toggleMF := func(variant multiface.MultifaceType) {
				if emu.peripherals.IsMultifaceEnabled() {
					emu.peripherals.DisableMultiface()
				} else {
					if err := emu.peripherals.EnableMultiface(variant, "roms"); err != nil {
						dialog.ShowError(fmt.Errorf("failed to enable %s: %w", multiface.GetVariantName(variant), err), w)
					}
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			mf1Item.Action = func() { toggleMF(multiface.Multiface1) }
			mf128Item.Action = func() { toggleMF(multiface.Multiface128) }
			mf3Item.Action = func() { toggleMF(multiface.Multiface3) }

			if1Item.Action = func() {
				if emu.peripherals.IsInterface1Enabled() {
					emu.disableInterface1()
				} else {
					if err := emu.enableInterface1(); err != nil {
						dialog.ShowError(fmt.Errorf("failed to enable Interface 1: %w", err), w)
					}
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			selectJoystick := func(t JoystickType) {
				// Release whatever's currently held on the active interface
				// so a held direction doesn't stick in the keyboard matrix
				// (Sinclair/Cursor) or as a Kempston port bit when switching.
				for dir := 0; dir < 5; dir++ {
					emu.dispatchJoystick(dir, false)
				}
				if emu.joystickType == JoystickKempston {
					emu.ula.KempstonEnabled = false
				}
				emu.joystickType = t
				if t == JoystickKempston {
					emu.ula.KempstonEnabled = true
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			joyNoneItem.Action = func() { selectJoystick(JoystickNone) }
			joyKempstonItem.Action = func() { selectJoystick(JoystickKempston) }
			joySinclair1Item.Action = func() { selectJoystick(JoystickSinclair1) }
			joySinclair2Item.Action = func() { selectJoystick(JoystickSinclair2) }
			joyCursorItem.Action = func() { selectJoystick(JoystickCursor) }

			kempMouseItem.Action = func() {
				if emu.peripherals.IsKempstonMouseEnabled() {
					emu.peripherals.DisableKempstonMouse()
				} else {
					emu.peripherals.EnableKempstonMouse()
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			zxPrinterItem.Action = func() {
				if emu.peripherals.IsZXPrinterEnabled() {
					emu.peripherals.DisableZXPrinter()
				} else {
					emu.peripherals.EnableZXPrinter()
				}
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			specDrumItem.Action = func() {
				if emu.speccyDAC == nil {
					return
				}
				emu.speccyDAC.SetSpecDrum(!emu.speccyDAC.SpecDrumEnabled())
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			covoxItem.Action = func() {
				if emu.speccyDAC == nil {
					return
				}
				emu.speccyDAC.SetCovox(!emu.speccyDAC.CovoxEnabled())
				saveConfig()
				fyne.Do(func() {
					updateLabels()
					w.MainMenu().Refresh()
				})
			}

			updateLabels()
			return fyne.NewMenu("Peripherals",
				discipleItem,
				fyne.NewMenuItemSeparator(),
				mf1Item, mf128Item, mf3Item,
				fyne.NewMenuItemSeparator(),
				if1Item,
				fyne.NewMenuItemSeparator(),
				joyNoneItem,
				joyKempstonItem,
				joySinclair1Item,
				joySinclair2Item,
				joyCursorItem,
				fyne.NewMenuItemSeparator(),
				kempMouseItem,
				zxPrinterItem,
				fyne.NewMenuItemSeparator(),
				specDrumItem,
				covoxItem,
			)
		}(),
		fyne.NewMenu("Emulator",
			fyne.NewMenuItem("Reboot", emu.reboot),
			fyne.NewMenuItem("Pause/Resume", emu.togglePause),
			fyne.NewMenuItem("Enter Poke...", func() {
				entry := widget.NewMultiLineEntry()
				entry.SetPlaceHolder("ADDR VALUE\n5C3A FF\n0x4000,0x55\n; comments allowed")
				entry.SetMinRowsVisible(8)
				form := dialog.NewCustomConfirm(
					"Enter Pokes",
					"Apply",
					"Cancel",
					container.NewBorder(
						widget.NewLabel("One poke per line. Address and value are hexadecimal."),
						nil, nil, nil,
						entry,
					),
					func(ok bool) {
						if !ok {
							return
						}
						pokes, perr := parsePokes(entry.Text)
						if perr != nil {
							dialog.ShowError(perr, w)
							return
						}
						for _, p := range pokes {
							emu.mem.Write(p.Addr, p.Val)
						}
						dialog.ShowInformation("Pokes Applied", fmt.Sprintf("Applied %d poke(s).", len(pokes)), w)
					},
					w,
				)
				form.Resize(fyne.NewSize(420, 320))
				form.Show()
			}),
			fyne.NewMenuItem("Debugger", func() {
				dbg := debugger.NewWithBreakpoints(emu.cpu, emu.mem, a, emu.sharedBreakpoints())
				// Share the register-watchpoint set with the telnet
				// debugger so a watch set on either surface fires and
				// is listed on both (the GUI Watchpoints tab).
				dbg.SetRegWatches(emu.sharedRegWatches())
				// Share the time-travel ring (GUI Time-Travel tab and
				// the telnet tt-* commands drive emu.timeTravel).
				dbg.SetTimeTravel(ttController{emu: emu})
				dbg.SetCallbacks(
					func() { emu.paused.Store(true) },
					func() { emu.cpu.StepInstruction() },
					func() { emu.paused.Store(false) },
					func() bool { return emu.paused.Load() },
				)
				// Step Over: run past a CALL/RST/PUSH-NN to its return,
				// else single-step. Runs synchronously while paused
				// (bounded so a non-returning call can't hang the UI),
				// reusing the same call-detection as telnet step-over.
				dbg.SetStepOver(func() {
					c := emu.cpu
					read := func(a uint16) byte { return emu.mem.Read(a) }
					lines := debugger.Disassemble(read, c.PC, 1)
					if len(lines) == 0 || len(lines[0].Bytes) == 0 ||
						!isCallLike(lines[0].Bytes[0], lines[0].Bytes) {
						c.StepInstruction()
						return
					}
					target := c.PC + uint16(len(lines[0].Bytes))
					const stepCap = 20_000_000
					for i := 0; i < stepCap; i++ {
						c.StepInstructionWithIRQ()
						if c.PC == target {
							break
						}
					}
				})
				// Wire breakpoints + register watchpoints: the CPU checks
				// both before each instruction and auto-pauses on a hit.
				emu.cpu.BreakpointCheck = func(pc uint16) bool {
					if dbg.CheckBreakpoint() || dbg.CheckWatchpoints() {
						emu.paused.Store(true)
						return true
					}
					return false
				}
				// Surface Spectrum Next state in the debugger when this
				// emulator instance was built as ModelNext.
				if currentModel == roms.ModelNext {
					dbg.SetNextProvider(newNextDebugProvider(emu))
					dbg.SetNextRegAccessor(emu.nextRegs)
				}
				// Hand the visual debugger the same BankAccessor the
				// telnet bank-peek / bank-poke commands use so the two
				// surfaces share behaviour.
				dbg.SetBankAccessor(&emuBanks{mem: emu.mem, emu: emu})
				// Wire M1-fetch history. If the telnet debugger
				// already started one, reuse it (single ring, two
				// surfaces); otherwise allocate a default-sized wide
				// ring for the visual debugger (interactive
				// investigation always wants IX/IY/HL visible).
				if emu.debugHistory == nil {
					emu.debugHistory = debugger.NewHistoryWide(4096)
					emu.cpu.AddPreFetchHook("visual-debugger-history", func(pc uint16) {
						c := emu.cpu
						emu.debugHistory.Push(debugger.HistoryEntry{
							PC: pc, SP: c.SP, A: c.A, F: c.F,
							IFFIM: debugger.PackIFFIM(c.IFF1, c.IFF2, c.Halted, int(c.IM)),
							Insns: c.InstructionCount(),
							BC:    c.BC(), DE: c.DE(), HL: c.HL(),
							IX: c.IX, IY: c.IY,
							Source: debugger.PCSource(c.BranchSource), SourceFrom: c.BranchFrom,
						})
						c.BranchSource = 0
						c.BranchFrom = 0
					})
				}
				dbg.SetHistory(emu.debugHistory)
				dbg.Show()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("ROM Info", func() {
				info := "Loaded ROMs:\n"
				for _, romType := range emu.mem.GetROMManager().GetLoadedROMs() {
					info += "• " + roms.GetROMTypeName(romType) + "\n"
				}
				info += "\nCurrent Model: " + roms.GetModelName(currentModel)
				dialog.ShowInformation("ROM Information", info, w)
			}),
			fyne.NewMenuItem("Peripheral Status", func() {
				status := emu.peripherals.GetStatus()
				info := "Peripheral Status:\n\n"

				if discipleEnabled, ok := status["disciple_enabled"].(bool); ok && discipleEnabled {
					info += "Disciple: Enabled\n"
					if romPaged, ok := status["disciple_rom_paged"].(bool); ok && romPaged {
						info += "  ROM: Paged In\n"
					} else {
						info += "  ROM: Paged Out\n"
					}
					if inhibited, ok := status["disciple_inhibited"].(bool); ok && inhibited {
						info += "  Status: Inhibited\n"
					}
				} else {
					info += "Disciple: Disabled\n"
				}

				if multifaceEnabled, ok := status["multiface_enabled"].(bool); ok && multifaceEnabled {
					if variant, ok := status["multiface_variant"].(string); ok {
						info += fmt.Sprintf("Multiface: %s\n", variant)
					}
					if romPaged, ok := status["multiface_rom_paged"].(bool); ok && romPaged {
						info += "  ROM: Paged In\n"
					} else {
						info += "  ROM: Paged Out\n"
					}
					if invisible, ok := status["multiface_invisible"].(bool); ok && invisible {
						info += "  Mode: Stealth\n"
					}
					if redButton, ok := status["multiface_red_button"].(bool); ok && redButton {
						info += "  Red Button: Pressed\n"
					}
				} else {
					info += "Multiface: Disabled\n"
				}

				info += "\nKeyboard Status: " + emu.kbd.GetKeyStatus()

				dialog.ShowInformation("Peripheral Status", info, w)
			}),
			fyne.NewMenuItem("Custom Keymap...", func() {
				// Read whatever override file is on disk so the dialog
				// reflects the user's most recent edits, even if they
				// changed it externally.
				path := userKeymapPath()
				if path == "" {
					dialog.ShowError(fmt.Errorf("could not determine home directory"), w)
					return
				}
				existing, _ := os.ReadFile(path)
				if len(existing) == 0 {
					existing = []byte("{\n  \"F1\": [{\"row\": 0, \"mask\": 1}, {\"row\": 7, \"mask\": 1}]\n}\n")
				}
				entry := widget.NewMultiLineEntry()
				entry.SetText(string(existing))
				entry.SetMinRowsVisible(12)
				help := widget.NewLabel(
					"JSON map of fyne key name -> matrix entries.\n" +
						"Each entry is {\"row\": 0..7, \"mask\": 1|2|4|8|16}.\n" +
						"Saved to " + path + " and applied immediately.",
				)
				form := dialog.NewCustomConfirm(
					"Custom Keymap",
					"Save & Apply",
					"Cancel",
					container.NewBorder(help, nil, nil, nil, entry),
					func(ok bool) {
						if !ok {
							return
						}
						// Validate JSON before writing.
						var raw map[string][]keyboard.MappingEntry
						if err := json.Unmarshal([]byte(entry.Text), &raw); err != nil {
							dialog.ShowError(fmt.Errorf("invalid keymap JSON: %w", err), w)
							return
						}
						// Ensure the directory exists, then write the file.
						if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
							dialog.ShowError(fmt.Errorf("create config dir: %w", err), w)
							return
						}
						if err := keyboard.SaveOverrides(path, raw); err != nil {
							dialog.ShowError(err, w)
							return
						}
						for name, entries := range raw {
							emu.kbd.SetMapping(fyne.KeyName(name), entries)
						}
						dialog.ShowInformation("Custom Keymap", fmt.Sprintf("Applied %d override(s).", len(raw)), w)
					},
					w,
				)
				form.Resize(fyne.NewSize(520, 420))
				form.Show()
			}),
			fyne.NewMenuItem("Trigger NMI (F12)", func() {
				emu.kbd.SimulateNMI()
			}),
		),
		fyne.NewMenu("Help",
			fyne.NewMenuItem("About zx_go", func() {
				dialog.ShowInformation("About zx_go",
					version.String()+"\n"+
						"built "+version.Date()+"\n\n"+
						"A hardware-faithful ZX Spectrum emulator\n"+
						"(48K / 128K / +2 / +2A / +3 / Next).\n\n"+
						"by "+version.Author+"\n"+
						version.RepoURL+"\n\n"+
						"The NextZXOS ROMs are licensed by their authors and\n"+
						"are not bundled; the emulator can download them from\n"+
						"the official Spectrum Next distribution on request.",
					w)
			}),
		),
	)
	w.SetMainMenu(mainMenu)

	// 4:3 aspect ratio container with black letterbox/pillarbox bars
	blackBG := canvas.NewRectangle(color.Black)
	aspectScreen := container.New(&aspectRatioLayout{ratio: 4.0 / 3.0}, screen)
	content := container.NewStack(blackBG, aspectScreen, keyboardWidget)

	// Show the splash artwork briefly, then swap to the real
	// content and grab keyboard focus. The emulation goroutine
	// starts running underneath the splash so by the time the
	// user is looking at the emulator the first frames are warm.
	emu.run(a, screen)
	emu.togglePause() // Start in a running state

	showSplash(w, func() {
		w.SetContent(content)
		w.Canvas().Focus(keyboardWidget)
	})

	// Set up cleanup on window close
	w.SetOnClosed(func() {
		emu.cleanup()
	})

	w.ShowAndRun()

	// GUI shutdown: persist opted-in SD writes (no-op unless
	// --sd-writeback and the guest actually wrote).
	emu.flushSDWriteback()
}
