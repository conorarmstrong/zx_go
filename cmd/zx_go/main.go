package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
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
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/microdrive"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/rzx"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

const (
	windowWidth     = 320 * 2
	windowHeight    = 240 * 2
	tstatesPerFrame = 69888
)

type keyState struct {
	key     fyne.KeyName
	pressed bool
}

// JoystickType identifies which joystick interface (if any) is currently
// active. Only one joystick interface can be active at a time.
type JoystickType int

const (
	JoystickNone     JoystickType = iota
	JoystickKempston              // Hardware port 0x1F (handled by ULA)
	JoystickSinclair1              // Sinclair Interface 2 left joystick (keys 1..5)
	JoystickSinclair2              // Sinclair Interface 2 right joystick (keys 6..0)
	JoystickCursor                 // Protek/Cursor joystick (keys 5/6/7/8/0)
)

type emulator struct {
	cpu         *z80.CPU
	mem         *memory.Memory
	ula         *ula.ULA
	kbd         *keyboard.Keyboard
	peripherals *peripherals.PeripheralManager

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

// keyboardWidget implements desktop.Keyable to receive KeyDown/KeyUp events
type keyboardWidget struct {
	widget.BaseWidget
	onKeyDown func(*fyne.KeyEvent)
	onKeyUp   func(*fyne.KeyEvent)
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
	// Ignore typed runes
}

func (kw *keyboardWidget) FocusGained() {}
func (kw *keyboardWidget) FocusLost()   {}

func (kw *keyboardWidget) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewWithoutLayout())
}

var _ desktop.Keyable = (*keyboardWidget)(nil)

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
		dialog.ShowInformation("Disk Loaded",
			"Inserted "+filepath.Base(path)+" into drive "+driveName+".", w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{
		".dsk", ".udi", ".mgt", ".img", ".trd", ".sad", ".d40", ".d80",
	}))
	fd.Show()
}

// userKeymapPath returns the absolute path to the user's keymap override
// file (~/.config/zxgo/keymap.json). It does not create the file or
// directory; the caller decides whether missing files matter.
func userKeymapPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "zxgo", "keymap.json")
}

func newEmulator(model roms.SpectrumModel) (*emulator, error) {
	kbd := keyboard.New()
	if path := userKeymapPath(); path != "" {
		if err := kbd.LoadOverrides(path); err != nil {
			log.Printf("Warning: failed to load custom keymap: %v", err)
		}
	}
	mem, err := memory.New("roms", model)
	if err != nil {
		return nil, err
	}
	ula := ula.New(mem, kbd)
	cpu := z80.New(mem, ula)

	// Initialize audio
	ula.EnableAudio()

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
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10),
		stopChan:     make(chan struct{}),
	}
	e.paused.Store(true)
	return e, nil
}

func (e *emulator) startKeyProcessor() {
	// Separate goroutine to process keyboard events
	// This prevents any blocking of the UI thread
	go func() {
		for {
			select {
			case ks := <-e.keyQueue:
				// Process the key state change
				e.kbd.HandleKeyWithModifiers(ks.key, ks.pressed, false, false, false, false)
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
	if e.joystickType != JoystickNone {
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

// dispatchJoystick translates a joystick direction (0=up..4=fire) into the
// appropriate hardware action for the active joystick interface. For
// Kempston this sets/clears a port bit; for Sinclair/Cursor it injects a
// Spectrum key press into the keyboard matrix.
func (e *emulator) dispatchJoystick(direction int, pressed bool) {
	switch e.joystickType {
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
		if mask != 0 {
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

func (e *emulator) handleKeyUp(ev *fyne.KeyEvent) {
	e.keyMutex.Lock()

	// Check if key was actually pressed
	if !e.physicalKeys[ev.Name] {
		e.keyMutex.Unlock()
		return
	}
	delete(e.physicalKeys, ev.Name)
	e.keyMutex.Unlock()

	// Joystick interception (release).
	if e.joystickType != JoystickNone {
		idx := joystickKeyForArrow(ev.Name)
		if idx >= 0 {
			e.dispatchJoystick(idx, false)
			return
		}
	}

	// Queue the key event (non-blocking)
	select {
	case e.keyQueue <- keyState{key: ev.Name, pressed: false}:
	default:
		// If queue is full, drop the event
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
					// Three execution paths: RZX playback,
					// RZX recording, or normal frame.
					switch {
					case e.rzxPlayback.Load() != nil:
						playback := e.rzxPlayback.Load()
						e.cpu.ExecuteRZXFrame(uint64(playback.Instructions()))
						snapBlock, err := playback.Frame()
						switch {
						case errors.Is(err, rzx.ErrPlaybackFinished):
							e.stopRZXPlayback()
						case err != nil:
							log.Printf("RZX playback error: %v", err)
							e.stopRZXPlayback()
						case snapBlock != nil:
							// Intermediate snapshot — apply it before the next frame.
							snap, derr := rzx.DecodeSnapshot(snapBlock)
							if derr != nil {
								log.Printf("RZX intermediate snapshot decode failed: %v; stopping playback", derr)
								e.stopRZXPlayback()
								break
							}
							if aerr := applySnapshotToEmulator(e, snap); aerr != nil {
								log.Printf("RZX intermediate snapshot apply failed: %v; stopping playback", aerr)
								e.stopRZXPlayback()
							}
						}
					case e.rzxRecord.Load() != nil:
						recorder := e.rzxRecord.Load()
						before := e.cpu.InstructionCount()
						e.cpu.ExecuteFrame(tstatesPerFrame)
						delta := e.cpu.InstructionCount() - before
						if delta > 0xFFFF {
							log.Printf("RZX record: frame instruction count %d > 0xFFFF, clamping", delta)
							delta = 0xFFFF
						}
						if err := recorder.StoreFrame(uint16(delta)); err != nil {
							log.Printf("RZX record StoreFrame: %v", err)
						}
						if recorder.AutosaveDue() {
							if snap, err := createSnapshotFromEmulator(e); err == nil {
								if block, err := rzx.EncodeSnapshot(snap, rzx.SnapshotFormatSZX, true); err == nil {
									recorder.AddAutosave(block, uint32(e.cpu.Tstates()))
								}
							}
						}
					default:
						e.cpu.ExecuteFrame(tstatesPerFrame)
					}

					frameCount++
					atomic.AddInt32(&e.frameCounter, 1)

					// Render at 50Hz
					now := time.Now()
					if now.Sub(lastRender) >= 20*time.Millisecond {
						newImage := e.ula.Render()

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
	log.Println("Rebooting emulator...")
	e.cpu.Reset()
	e.ula.Reset()

	// Clear key states on reboot
	e.keyMutex.Lock()
	e.physicalKeys = make(map[fyne.KeyName]bool)
	e.keyMutex.Unlock()

	// Drain key queue
	for len(e.keyQueue) > 0 {
		<-e.keyQueue
	}
}

func (e *emulator) togglePause() {
	e.paused.Store(!e.paused.Load())
	if e.paused.Load() {
		log.Println("Emulator paused")
	} else {
		log.Println("Emulator resumed")
	}
}

func (e *emulator) cleanup() {
	log.Println("Cleaning up emulator resources...")
	e.paused.Store(true)
	close(e.stopChan)
	if e.ticker != nil {
		e.ticker.Stop()
	}
	e.ula.Close()
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

// applySnapshotToEmulator applies a loaded snapshot to the running emulator
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
			log.Printf("RZX stop: final snapshot capture failed: %v", err)
		}
	}

	if err := rec.WriteFile(filename, rzx.WriteOptions{Compress: true}); err != nil {
		return fmt.Errorf("rzx write %s: %w", filename, err)
	}
	return nil
}

// makeMicrodriveMenu builds an 8-drive Microdrive submenu. Each child
// is itself a submenu with Insert / Save / Eject / Write Protect.
// The whole tree disables itself when the IF1 isn't enabled, so
// users get a clear visual cue that they need to enable the
// peripheral first.
func makeMicrodriveMenu(emu *emulator, w fyne.Window) *fyne.MenuItem {
	root := fyne.NewMenuItem("Microdrives", nil)

	driveItems := make([]*fyne.MenuItem, microdriveSlotCount)
	for i := 0; i < microdriveSlotCount; i++ {
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
				cart, err := microdrive.ReadFile(path)
				if err != nil {
					dialog.ShowError(fmt.Errorf("load microdrive: %w", err), w)
					return
				}
				if err := emu.peripherals.InsertMicrodrive(slot, cart); err != nil {
					dialog.ShowError(err, w)
					return
				}
				dialog.ShowInformation("Microdrive", fmt.Sprintf("Cartridge loaded into Drive %d:\n%s", slot+1, filepath.Base(path)), w)
			}, w)
			fd.SetFilter(storage.NewExtensionFileFilter([]string{".mdr"}))
			fd.Show()
		})
		save := fyne.NewMenuItem("Save Cartridge...", func() {
			dev := emu.peripherals.IF1()
			if dev == nil {
				dialog.ShowInformation("Microdrive", "Interface 1 is not enabled.", w)
				return
			}
			d := dev.ULA.Bus.Drive(slot)
			if d == nil || d.Cartridge == nil {
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
				if err := d.Cartridge.WriteFile(path); err != nil {
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
			dev := emu.peripherals.IF1()
			if dev == nil {
				return
			}
			d := dev.ULA.Bus.Drive(slot)
			if d == nil || d.Cartridge == nil {
				return
			}
			d.Cartridge.SetWriteProtect(!d.Cartridge.WriteProtect())
		})

		driveItem := fyne.NewMenuItem(fmt.Sprintf("Drive %d", slot+1), nil)
		driveItem.ChildMenu = fyne.NewMenu("", insert, save, eject, wp)
		driveItems[i] = driveItem
	}
	root.ChildMenu = fyne.NewMenu("", driveItems...)
	return root
}

// microdriveSlotCount is the number of microdrive slots the Interface
// 1 supports — duplicated here from if1.NumDrives so the menu code
// doesn't need to import pkg/if1 directly.
const microdriveSlotCount = 8

// enableInterface1 turns on the Interface 1 — pulls the IF1 ROM from
// the ROM manager (which loads it from roms/if1-2.rom or the embedded
// fallback), enables the IF1 in the peripheral manager, and installs
// the per-instruction page-in/page-out hooks on the Z80. Returns an
// error with a user-friendly message if the ROM is missing.
func (e *emulator) enableInterface1() error {
	rom, ok := e.mem.GetROMManager().GetROM(roms.ROMINTERFACE1)
	if !ok {
		return fmt.Errorf("interface 1 ROM not found — drop if1-2.rom (8KB) into the roms/ directory; available from World of Spectrum and similar archives")
	}
	if err := e.peripherals.EnableInterface1(rom); err != nil {
		return err
	}
	dev := e.peripherals.IF1()
	e.cpu.PreFetchHook = dev.PreFetchHook
	e.cpu.PostFetchHook = dev.PostFetchHook
	return nil
}

// disableInterface1 tears down the Interface 1: removes the Z80 page
// hooks and disables the device in the peripheral manager.
func (e *emulator) disableInterface1() {
	e.cpu.PreFetchHook = nil
	e.cpu.PostFetchHook = nil
	e.peripherals.DisableInterface1()
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
// On entry to LD-BYTES the contract is:
//   A'         expected flag byte (header=0x00, data=0xFF)
//   F' carry   set means LOAD, clear means VERIFY
//   IX         destination address
//   DE         number of bytes to load (excluding flag/checksum)
//
// The routine returns with carry set on success, carry clear on failure.
// We replicate this contract by reading bytes directly from the current
// tape block (which is stored as: flag byte, data bytes..., checksum byte).
func installTapeTrap(emu *emulator) {
	emu.cpu.TrapCheck = func(pc uint16) bool {
		if pc != 0x0556 {
			return false
		}
		// Only on 48K (the entry point differs on later models, and the ROM
		// is paged differently). Other models still get correct behaviour
		// via the slow tape player.
		if emu.mem.GetCurrentModel() != roms.Model48K {
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

		// Use A' as the expected flag (the routine swaps AF/AF' on entry).
		expectedFlag := emu.cpu.A_
		// Carry of F' = 1 means LOAD, 0 means VERIFY.
		isLoad := (emu.cpu.F_ & z80.FLAG_C) != 0

		dst := emu.cpu.IX
		count := uint16(emu.cpu.D)<<8 | uint16(emu.cpu.E)

		success := true
		if len(block) < 1 {
			success = false
		} else if block[0] != expectedFlag {
			// Flag mismatch — emulate failure.
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

		log.Printf("Tape trap: loaded %d bytes at 0x%04X (success=%v)", count, dst, success)
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

func main() {
	a := app.NewWithID("com.conorarmstrong.zxgo")
	a.SetIcon(spectrumIcon())

	// Start with 48K model by default
	currentModel := roms.Model48K

	w := a.NewWindow(fmt.Sprintf("ZX Spectrum Emulator - %s", roms.GetModelName(currentModel)))
	w.SetIcon(spectrumIcon())
	w.Resize(fyne.NewSize(windowWidth, windowHeight))

	emu, err := newEmulator(currentModel)
	if err != nil {
		log.Fatalf("Failed to create emulator: %v", err)
	}
	emu.window = w

	// Install fast tape loading trap (no-op until a tape is loaded).
	installTapeTrap(emu)

	// Use static image approach
	initialImage := emu.ula.Render()
	screen := canvas.NewImageFromImage(initialImage)
	screen.ScaleMode = canvas.ImageScalePixels

	// Create keyboard widget with event handlers
	keyboardWidget := newKeyboardWidget(
		emu.handleKeyDown,
		emu.handleKeyUp,
	)

	// Create model selection callback
	switchModel := func(newModel roms.SpectrumModel) {
		log.Printf("Switching to %s...", roms.GetModelName(newModel))

		// Pause emulation during switch
		wasPaused := emu.paused.Load()
		if !emu.paused.Load() {
			emu.togglePause()
		}

		if err := emu.mem.SwitchModel(newModel); err != nil {
			log.Printf("Failed to switch model: %v", err)
			dialog.ShowError(fmt.Errorf("failed to switch to %s: %w", roms.GetModelName(newModel), err), w)
			// Restore previous state
			if !wasPaused {
				emu.togglePause()
			}
			return
		}

		currentModel = newModel

		// Automatic reboot after model switch
		emu.reboot()

		// Update window title to show current model
		w.SetTitle(fmt.Sprintf("ZX Spectrum Emulator - %s", roms.GetModelName(currentModel)))

		// Resume emulation if it was running
		if !wasPaused {
			emu.togglePause()
		}

		log.Printf("Successfully switched to %s", roms.GetModelName(currentModel))
		dialog.ShowInformation("Model Changed", fmt.Sprintf("Successfully switched to %s\n\nThe emulator has been automatically rebooted with the new ROM.", roms.GetModelName(currentModel)), w)
	}

	mainMenu := fyne.NewMainMenu(
		fyne.NewMenu("File",
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

					log.Printf("Loaded ROM: %s (%d bytes)", romPath, len(data))
					dialog.ShowInformation("ROM Loaded", fmt.Sprintf("Loaded %s\n(%d bytes)\n\nEmulator rebooted.", reader.URI().Name(), len(data)), w)
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".rom"}))
				fd.Show()
			}),
			fyne.NewMenuItem("Load Snapshot...", func() {
				log.Println("Load Snapshot...")
				fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if reader == nil {
						return
					}
					log.Println("Snapshot selected:", reader.URI().Path())

					// Load the snapshot
					snap := snapshot.New()
					if err := snap.Load(reader.URI().Path()); err != nil {
						dialog.ShowError(fmt.Errorf("failed to load snapshot: %w", err), w)
						_ = reader.Close()
						return
					}

					// Apply snapshot to emulator
					if err := applySnapshotToEmulator(emu, snap); err != nil {
						dialog.ShowError(fmt.Errorf("failed to apply snapshot: %w", err), w)
						_ = reader.Close()
						return
					}

					log.Printf("Successfully loaded %s snapshot", getFormatName(snap.Format))
					dialog.ShowInformation("Snapshot Loaded", fmt.Sprintf("Successfully loaded %s snapshot from:\n%s", getFormatName(snap.Format), reader.URI().Name()), w)
					_ = reader.Close()
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".z80", ".sna", ".szx"}))
				fd.Show()
			}),
			fyne.NewMenuItem("Save Snapshot...", func() {
				log.Println("Save Snapshot...")
				fd := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if writer == nil {
						return
					}
					log.Println("Snapshot save location:", writer.URI().Path())

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

					log.Printf("Successfully saved %s snapshot", getFormatName(snap.Format))
					dialog.ShowInformation("Snapshot Saved", fmt.Sprintf("Successfully saved %s snapshot to:\n%s", getFormatName(snap.Format), writer.URI().Name()), w)
					_ = writer.Close()
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".z80", ".sna", ".szx"}))
				fd.Show()
			}),
			fyne.NewMenuItemSeparator(),
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
					dialog.ShowInformation("Tape Loaded", fmt.Sprintf("Loaded %d blocks from:\n%s\n\nTape is now playing.", tp.BlockCount(), reader.URI().Name()), w)
					_ = reader.Close()
				}, w)
				fd.SetFilter(storage.NewExtensionFileFilter([]string{".tzx"}))
				fd.Show()
			}),
			fyne.NewMenuItem("Load Disk A...", func() {
				loadPlus3Disk(emu, w, currentModel, 0)
			}),
			fyne.NewMenuItem("Load Disk B...", func() {
				loadPlus3Disk(emu, w, currentModel, 1)
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
			fyne.NewMenuItemSeparator(),
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
			fyne.NewMenuItemSeparator(),
			makeMicrodriveMenu(emu, w),
			fyne.NewMenuItemSeparator(),
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
			fyne.NewMenuItem("Stop Tape", func() {
				if emu.ula != nil {
					emu.ula.SetTapePlayer(nil)
					emu.ula.TapeIn = false
				}
			}),
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
					defer func() { _ = writer.Close() }()

					// Render the current frame and copy the pixel data so the
					// PNG encode can't race with the emulator goroutine.
					src := emu.ula.Render()
					imgCopy := image.NewRGBA(src.Bounds())
					copy(imgCopy.Pix, src.Pix)

					if err := png.Encode(writer, imgCopy); err != nil {
						dialog.ShowError(fmt.Errorf("failed to write PNG: %w", err), w)
						return
					}
					dialog.ShowInformation("Screenshot Saved", "Saved screenshot to:\n"+writer.URI().Name(), w)
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
		),
		fyne.NewMenu("View",
			fyne.NewMenuItem("100% (320x240)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(320, 240))
			}),
			fyne.NewMenuItem("125% (400x300)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(400, 300))
			}),
			fyne.NewMenuItem("150% (480x360)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(480, 360))
			}),
			fyne.NewMenuItem("200% (640x480)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(640, 480))
			}),
			fyne.NewMenuItem("300% (960x720)", func() {
				w.SetFullScreen(false)
				w.Resize(fyne.NewSize(960, 720))
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Full Screen", func() {
				w.SetFullScreen(true)
			}),
			func() *fyne.MenuItem {
				item := fyne.NewMenuItem("Enable CRT Filter", nil)
				item.Action = func() {
					on := !emu.crtFilter.Load()
					emu.crtFilter.Store(on)
					if on {
						item.Label = "Disable CRT Filter"
					} else {
						item.Label = "Enable CRT Filter"
					}
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
			}

			discipleItem.Action = func() {
				if emu.peripherals.IsDiscipleEnabled() {
					emu.peripherals.DisableDisciple()
				} else {
					if err := emu.peripherals.EnableDisciple("roms"); err != nil {
						dialog.ShowError(fmt.Errorf("failed to enable Disciple: %w", err), w)
					}
				}
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
				dbg := debugger.New(emu.cpu, emu.mem, a)
				dbg.SetCallbacks(
					func() { emu.paused.Store(true) },
					func() { emu.cpu.StepInstruction() },
					func() { emu.paused.Store(false) },
					func() bool { return emu.paused.Load() },
				)
				// Wire breakpoints: when the debugger has a breakpoint set,
				// the CPU checks it before each instruction and auto-pauses.
				emu.cpu.BreakpointCheck = func(pc uint16) bool {
					if dbg.CheckBreakpoint() {
						emu.paused.Store(true)
						return true
					}
					return false
				}
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
	)
	w.SetMainMenu(mainMenu)

	// 4:3 aspect ratio container with black letterbox/pillarbox bars
	blackBG := canvas.NewRectangle(color.Black)
	aspectScreen := container.New(&aspectRatioLayout{ratio: 4.0 / 3.0}, screen)
	content := container.NewStack(blackBG, aspectScreen, keyboardWidget)
	w.SetContent(content)

	// Make sure the keyboard widget gets focus
	w.Canvas().Focus(keyboardWidget)

	emu.run(a, screen)
	emu.togglePause() // Start in a running state

	// Set up cleanup on window close
	w.SetOnClosed(func() {
		emu.cleanup()
	})

	w.ShowAndRun()
}
