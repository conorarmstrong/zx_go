// Package testharness provides a headless, scripted ZX Spectrum
// emulator for end-to-end integration tests. It wires the existing
// pkg/{z80,memory,ula,keyboard,peripherals,if1,...} pieces together
// without depending on Fyne or any GUI runtime, and exposes a small
// API tailored for tests:
//
//   - construction with a model and the embedded peripheral ROMs
//   - synchronous frame stepping (no goroutines, deterministic)
//   - key press/release/tap/Type for input scripting
//   - memory peek/poke
//   - screen image capture and (in screen.go) text extraction via OCR
//   - NMI / reset / snapshot loading / Interface 1 enable / .mdr insert
//
// The harness is the canonical way to validate behaviour that
// requires actual ROM execution — e.g. "load this microdrive
// cartridge, run the IF1's CAT command, and check the screen for
// the expected file list" — which can't be done with the per-package
// unit tests.
package testharness

import (
	"fmt"
	"image"
	"time"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/dac"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/esxdos"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// TstatesPerFrame is the 48K Spectrum's frame length used by the
// harness. Other models tick at slightly different rates, but
// 69888 is fine for any test that doesn't care about exact pitch.
const TstatesPerFrame = 69888

// Harness is a headless Spectrum emulator wrapped in a scripting
// API for tests. Construct one with New, drive it with the methods
// below. The CPU runs synchronously inside RunFrames — there is no
// goroutine, no ticker, no real-time pacing — so tests are
// deterministic and can step through behaviour one frame at a time.
type Harness struct {
	cpu         *z80.CPU
	mem         *memory.Memory
	ula         *ula.ULA
	kbd         *keyboard.Keyboard
	peripherals *peripherals.PeripheralManager

	// nextEsxdos is the Spectrum Next's esxDOS dispatcher when
	// the harness was constructed with ModelNext, nil otherwise.
	// MountSDCard / LoadNEX reach in here for SD-backed APIs.
	nextEsxdos *esxdos.Dispatcher

	// nextDivMMC is the Spectrum Next's divMMC pager when the
	// harness was constructed with ModelNext, nil otherwise.
	// MountSDCard reaches in here to enable automap so the
	// esxDOS RST $08 hook will fire for tests that synthesise an
	// API call without booting through the firmware first.
	nextDivMMC *divmmc.Pager

	// nextDAC is the Spectrum Next's four-channel DAC bank when
	// ModelNext, nil otherwise. Tests reach in here to verify the
	// mixer contribution after exercising the CPU/ULA port path.
	nextDAC *dac.Bank

	// nextSprites is the Spectrum Next's hardware sprite engine when
	// ModelNext, nil otherwise. Tests reach in here to verify sprite
	// attribute/pattern state after exercising the $303B/$5B/$57
	// port-write path.
	nextSprites *sprite.Engine
}

// New constructs a fresh Harness for the given Spectrum model. The
// peripheral ROMs are loaded from the embedded ROM filesystem (the
// same path memory.New uses), so no on-disk ROM directory is
// required. Returns an error only if the embedded ROM load fails —
// in practice this means the binary was built without one of the
// required ROMs in pkg/roms/data, which is a build-time issue.
func New(model roms.SpectrumModel) (*Harness, error) {
	if model == roms.ModelNext {
		return newNext()
	}
	mem, err := memory.New("", model)
	if err != nil {
		return nil, fmt.Errorf("testharness: memory init: %w", err)
	}
	kbd := keyboard.New()
	u := ula.New(mem, kbd)
	cpu := z80.New(mem, u)
	pm := peripherals.NewPeripheralManager(mem, "")
	u.SetPeripherals(pm)
	mem.PeripheralRead = pm.HandleMemoryRead
	mem.PeripheralWrite = pm.HandleMemoryWrite

	h := &Harness{
		cpu:         cpu,
		mem:         mem,
		ula:         u,
		kbd:         kbd,
		peripherals: pm,
	}
	return h, nil
}

// CPU returns the underlying Z80. Tests that need to peek at
// register state directly can use it; most tests should stick to
// the higher-level Memory / ScreenText helpers.
func (h *Harness) CPU() *z80.CPU { return h.cpu }

// CloseFiles closes any open esxDOS host-file handles. Tests should call
// it (via t.Cleanup) before their t.TempDir is removed — on Windows an
// unclosed handle blocks deletion of the backing file. No-op when the
// Next esxDOS dispatcher isn't wired (non-Next models).
func (h *Harness) CloseFiles() {
	if h.nextEsxdos != nil {
		h.nextEsxdos.CloseAll()
	}
}

// Memory returns the underlying memory.Memory.
func (h *Harness) MemoryBus() *memory.Memory { return h.mem }

// ULA returns the underlying ULA. Most tests should use the
// ScreenImage / ScreenText helpers instead of touching this directly.
func (h *Harness) ULA() *ula.ULA { return h.ula }

// Peripherals returns the peripheral manager so tests can
// enable/disable interfaces, insert media, etc.
func (h *Harness) Peripherals() *peripherals.PeripheralManager { return h.peripherals }

// Reboot performs a hard reset: CPU registers cleared, ULA reset,
// memory paging restored to the current model's power-on defaults.
// Inserted media (microdrive cartridges, +3 disks, IF2 cartridges) are
// NOT removed; RAM is NOT cleared. The Spectrum boot sequence will run
// on the next RunFrames call.
func (h *Harness) Reboot() {
	h.cpu.Reset()
	h.ula.Reset()
	_ = h.mem.Reset()
}

// RunFrames executes n full emulator frames synchronously on the
// calling goroutine. Each frame runs the CPU for TstatesPerFrame
// T-states, renders the screen, and ticks per-frame peripherals
// (e.g. the ZX Printer drum timer). Use this to advance the
// emulator past boot, between key presses, or while waiting for
// asynchronous BASIC operations to complete.
func (h *Harness) RunFrames(n int) {
	for i := 0; i < n; i++ {
		h.cpu.ExecuteFrame(TstatesPerFrame)
		h.ula.Render()
		h.peripherals.Frame()
	}
}

// RunUntil runs frames until pred returns true or maxFrames have
// elapsed. Returns nil on success or a timeout error containing
// the elapsed frame count.
func (h *Harness) RunUntil(pred func(*Harness) bool, maxFrames int) error {
	for i := 0; i < maxFrames; i++ {
		if pred(h) {
			return nil
		}
		h.RunFrames(1)
	}
	if pred(h) {
		return nil
	}
	return fmt.Errorf("testharness: predicate not satisfied within %d frames", maxFrames)
}

// PressKey marks a host key as held down. The key is mapped to the
// Spectrum keyboard matrix the same way it is in the GUI — you can
// use any fyne.KeyName the keyboard package recognises (fyne.KeyA,
// fyne.KeyEnter, fyne.KeySpace, etc.). Held until ReleaseKey.
func (h *Harness) PressKey(key fyne.KeyName) {
	h.kbd.HandleKeyWithModifiers(key, true, false, false, false, false)
}

// PressKeyShift marks a host key as held down with the host shift
// modifier active — i.e. the Spectrum CAPS SHIFT bit gets set
// alongside the base key. Used for symbols, capital letters in
// some BASIC keyword contexts, and the dedicated SHIFT-ed Spectrum
// keys (DELETE = SHIFT+0, etc.).
func (h *Harness) PressKeyShift(key fyne.KeyName) {
	h.kbd.HandleKeyWithModifiers(key, true, true, false, false, false)
}

// ReleaseKey marks a host key as released.
func (h *Harness) ReleaseKey(key fyne.KeyName) {
	h.kbd.HandleKeyWithModifiers(key, false, false, false, false, false)
}

// ReleaseKeyShift releases a key that was pressed with PressKeyShift.
func (h *Harness) ReleaseKeyShift(key fyne.KeyName) {
	h.kbd.HandleKeyWithModifiers(key, false, true, false, false, false)
}

// TapKey presses a key, runs holdFrames frames, releases it, then
// runs gapFrames more frames. The default values (3 hold + 2 gap)
// give the Spectrum's keyboard scan plenty of time to register the
// keypress without dragging out per-character cost.
func (h *Harness) TapKey(key fyne.KeyName) {
	h.PressKey(key)
	h.RunFrames(3)
	h.ReleaseKey(key)
	h.RunFrames(2)
}

// TapKeyShift is TapKey with the SHIFT modifier held — i.e. CAPS
// SHIFT is asserted on the Spectrum keyboard matrix.
func (h *Harness) TapKeyShift(key fyne.KeyName) {
	h.PressKeyShift(key)
	h.RunFrames(3)
	h.ReleaseKeyShift(key)
	h.RunFrames(2)
}

// PressCAPSShift sets or releases the CAPS SHIFT bit on the
// Spectrum keyboard matrix directly. Used to drive extended
// keyword entry sequences where the host modifier flags can't
// cleanly express "CAPS SHIFT alone without any base key".
func (h *Harness) PressCAPSShift(pressed bool) {
	h.kbd.PressMatrixKey(0, 0x01, pressed)
}

// PressSymbolShift sets or releases the SYMBOL SHIFT bit directly.
// Companion to PressCAPSShift for E-mode keyword entry.
func (h *Harness) PressSymbolShift(pressed bool) {
	h.kbd.PressMatrixKey(7, 0x02, pressed)
}

// EnterExtendedMode briefly presses CAPS SHIFT + SYMBOL SHIFT to
// switch the BASIC cursor into E (extended) mode. The next tapped
// key is then interpreted as an extended keyword.
func (h *Harness) EnterExtendedMode() {
	h.PressCAPSShift(true)
	h.PressSymbolShift(true)
	h.RunFrames(3)
	h.PressCAPSShift(false)
	h.PressSymbolShift(false)
	h.RunFrames(2)
}

// NMI signals a non-maskable interrupt to the CPU, the same way
// the Multiface red button does. The CPU services it on the next
// instruction boundary inside RunFrames.
func (h *Harness) NMI() {
	h.cpu.PendingNMI.Store(true)
}

// Memory reads a single byte from the CPU's memory space at addr.
// ROM and RAM areas both work; the value reflects the current
// page mapping (so a 128K test that paged a different bank into
// 0xC000 sees that bank).
func (h *Harness) Memory(addr uint16) byte { return h.mem.Read(addr) }

// WriteMemory writes a single byte. Writes to ROM areas are
// silently ignored, the same as the real CPU.
func (h *Harness) WriteMemory(addr uint16, val byte) { h.mem.Write(addr, val) }

// ScreenImage returns the current rendered screen image. The
// returned RGBA pointer is owned by the ULA and may be overwritten
// by the next Render call (i.e. the next RunFrames invocation), so
// callers that need a stable copy should clone it.
func (h *Harness) ScreenImage() *image.RGBA { return h.ula.Render() }

// SaveScreenshot captures the current screen and writes it as a
// PNG to path. Useful for human debugging when an automated test
// fails — pop the saved image into a viewer to see what the
// emulator was actually showing.
func (h *Harness) SaveScreenshot(path string) error {
	img := h.ScreenImage()
	return savePNG(path, img)
}

// LoadSnapshot loads a .sna / .z80 / .szx snapshot from disk and
// applies it to the running emulator. The current model must
// match the snapshot's model (the harness doesn't auto-switch).
func (h *Harness) LoadSnapshot(path string) error {
	return applySnapshotFile(h, path)
}

// EnableInterface1 turns on the Sinclair Interface 1 — the same
// path the GUI takes when the user clicks "Enable Interface 1".
// Pulls the embedded if1-2.rom and installs the page-in/page-out
// hooks on the CPU. Returns an error if the IF1 ROM isn't
// available or the current model isn't 48K (IF1 is 48K-only).
func (h *Harness) EnableInterface1() error {
	rom, ok := h.mem.GetROMManager().GetROM(roms.ROMINTERFACE1)
	if !ok {
		return fmt.Errorf("testharness: IF1 ROM not loaded")
	}
	if err := h.peripherals.EnableInterface1(rom); err != nil {
		return err
	}
	dev := h.peripherals.IF1()
	h.cpu.PreFetchHook = dev.PreFetchHook
	h.cpu.PostFetchHook = dev.PostFetchHook
	return nil
}

// InsertMicrodrive inserts the cartridge file at path into the
// IF1's drive `slot` (0-based). Requires that EnableInterface1
// has already been called.
func (h *Harness) InsertMicrodrive(slot int, path string) error {
	return h.peripherals.LoadMicrodrive(slot, path)
}

// InsertInterface2Cartridge loads a 16KB .rom cartridge image and
// inserts it into the Interface 2 cartridge slot, then resets the
// CPU so the cartridge code starts at PC=0x0000. Only works in
// 48K mode — returns an error on any other model.
func (h *Harness) InsertInterface2Cartridge(path string) error {
	if err := h.peripherals.InsertInterface2Cartridge(path); err != nil {
		return err
	}
	h.cpu.Reset()
	return nil
}

// RemoveInterface2Cartridge ejects any inserted IF2 cartridge and
// resets the CPU back into BASIC.
func (h *Harness) RemoveInterface2Cartridge() {
	h.peripherals.RemoveInterface2Cartridge()
	h.cpu.Reset()
}

// EnableDisciple enables the DISCiPLE disk interface with auto-paging
// hooks on the CPU. The DISCiPLE starts paged out (safe for mid-session
// enable). Use EnableDiscipleColdBoot to simulate power-on with the
// DISCiPLE installed.
func (h *Harness) EnableDisciple() error {
	if err := h.peripherals.EnableDisciple("roms"); err != nil {
		return err
	}
	dev := h.peripherals.GetDisciple()
	h.cpu.PreFetchHook = dev.PreFetchHook
	h.cpu.PostFetchHook = dev.PostFetchHook
	return nil
}

// EnableDiscipleColdBoot enables the DISCiPLE and pages it in,
// simulating a power-on with the DISCiPLE installed. The CPU should
// be at PC=0 (fresh reset) so the GDOS boot code at ROM 0x0000 runs.
func (h *Harness) EnableDiscipleColdBoot() error {
	if err := h.EnableDisciple(); err != nil {
		return err
	}
	// Page in so GDOS boot code runs from 0x0000
	h.peripherals.GetDisciple().HandlePortRead(0xBB)
	return nil
}

// LoadDiscipleDisk loads a disk image into the specified DISCiPLE drive.
func (h *Harness) LoadDiscipleDisk(drive int, path string) error {
	return h.peripherals.LoadDiscipleDisk(drive, path)
}

// Wait runs frames continuously for the given wall-clock duration
// at the simulated 50Hz frame rate (i.e. d / 20ms frames). Used
// for tests that just need "let the emulator settle for half a
// second" without caring about exact frame counts.
func (h *Harness) Wait(d time.Duration) {
	frames := int(d.Milliseconds() / 20)
	if frames < 1 {
		frames = 1
	}
	h.RunFrames(frames)
}
