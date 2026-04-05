package main

import (
	"fmt"
	"log"
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
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
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

type emulator struct {
	cpu         *z80.CPU
	mem         *memory.Memory
	ula         *ula.ULA
	kbd         *keyboard.Keyboard
	peripherals *peripherals.PeripheralManager

	paused bool
	ticker *time.Ticker

	// Track physical key states to prevent OS repeat issues
	physicalKeys map[fyne.KeyName]bool
	keyMutex     sync.Mutex

	// Frame counter
	frameCounter int32

	// Separate goroutine for processing keys
	keyQueue chan keyState
	stopChan chan struct{}
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

func newEmulator(model roms.SpectrumModel) (*emulator, error) {
	kbd := keyboard.New()
	mem, err := memory.New("roms", model)
	if err != nil {
		return nil, err
	}
	ula := ula.New(mem, kbd)
	cpu := z80.New(mem, ula)

	// Initialize audio
	ula.EnableAudio()

	// Create peripheral manager and wire it to ULA
	pm := peripherals.NewPeripheralManager(mem, "roms")
	ula.SetPeripherals(pm)

	// Set up NMI callback for keyboard (Multiface red button simulation)
	kbd.SetNMICallback(func() {
		// Check if Multiface is enabled and handle NMI
		if pm.IsMultifaceEnabled() {
			pm.HandleNMI()
			cpu.NMI() // Trigger NMI on CPU
		}
	})

	return &emulator{
		cpu:          cpu,
		mem:          mem,
		ula:          ula,
		kbd:          kbd,
		peripherals:  pm,
		paused:       true,
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10), // Small buffer
		stopChan:     make(chan struct{}),
	}, nil
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
	e.keyMutex.Lock()

	// Check if this is a repeat event from the OS
	if e.physicalKeys[ev.Name] {
		e.keyMutex.Unlock()
		return // Ignore repeat
	}
	e.physicalKeys[ev.Name] = true
	e.keyMutex.Unlock()

	// Queue the key event (non-blocking)
	select {
	case e.keyQueue <- keyState{key: ev.Name, pressed: true}:
	default:
		// If queue is full, drop the event (shouldn't happen with normal typing)
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
				if !e.paused {
					// Execute CPU frame
					e.cpu.ExecuteFrame(tstatesPerFrame)

					frameCount++
					atomic.AddInt32(&e.frameCounter, 1)

					// Render at 50Hz
					now := time.Now()
					if now.Sub(lastRender) >= 20*time.Millisecond {
						newImage := e.ula.Render()

						// Update UI on main thread
						fyne.Do(func() {
							newImageObj := canvas.NewImageFromImage(newImage)
							newImageObj.ScaleMode = canvas.ImageScalePixels
							screen.Resource = newImageObj.Resource
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
	e.paused = !e.paused
	if e.paused {
		log.Println("Emulator paused")
	} else {
		log.Println("Emulator resumed")
	}
}

func (e *emulator) cleanup() {
	log.Println("Cleaning up emulator resources...")
	e.paused = true
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
	wasPaused := emu.paused
	if !emu.paused {
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

func main() {
	a := app.NewWithID("com.conorarmstrong.zxgo")

	// Start with 48K model by default
	currentModel := roms.Model48K

	w := a.NewWindow(fmt.Sprintf("ZX Spectrum Emulator - %s", roms.GetModelName(currentModel)))
	w.Resize(fyne.NewSize(windowWidth, windowHeight))
	w.SetFixedSize(true)

	emu, err := newEmulator(currentModel)
	if err != nil {
		log.Fatalf("Failed to create emulator: %v", err)
	}

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
		wasPaused := emu.paused
		if !emu.paused {
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
			fyne.NewMenuItem("Select ROM...", func() {
				log.Println("Select ROM...")
				fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if reader == nil {
						return
					}
					log.Println("ROM selected:", reader.URI().Path())
					// TODO: Load ROM
					_ = reader.Close()
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
			fyne.NewMenuItem("Stop Tape", func() {
				if emu.ula != nil {
					emu.ula.SetTapePlayer(nil)
					emu.ula.TapeIn = false
				}
			}),
		),
		fyne.NewMenu("Machine",
			fyne.NewMenuItem("48K", func() { switchModel(roms.Model48K) }),
			fyne.NewMenuItem("128K", func() { switchModel(roms.Model128K) }),
			fyne.NewMenuItem("+2", func() { switchModel(roms.ModelPlus2) }),
			fyne.NewMenuItem("+2A", func() { switchModel(roms.ModelPlus2A) }),
			fyne.NewMenuItem("+3", func() { switchModel(roms.ModelPlus3) }),
		),
		fyne.NewMenu("Peripherals",
			fyne.NewMenuItem("Enable Disciple", func() {
				if err := emu.peripherals.EnableDisciple("roms"); err != nil {
					dialog.ShowError(fmt.Errorf("failed to enable Disciple: %w", err), w)
				} else {
					dialog.ShowInformation("Success", "Disciple disk interface enabled", w)
				}
			}),
			fyne.NewMenuItem("Disable Disciple", func() {
				emu.peripherals.DisableDisciple()
				dialog.ShowInformation("Success", "Disciple disk interface disabled", w)
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Enable Multiface 1", func() {
				if err := emu.peripherals.EnableMultiface(multiface.Multiface1, "roms"); err != nil {
					dialog.ShowError(fmt.Errorf("failed to enable Multiface 1: %w", err), w)
				} else {
					dialog.ShowInformation("Success", "Multiface 1 enabled", w)
				}
			}),
			fyne.NewMenuItem("Enable Multiface 128", func() {
				if err := emu.peripherals.EnableMultiface(multiface.Multiface128, "roms"); err != nil {
					dialog.ShowError(fmt.Errorf("failed to enable Multiface 128: %w", err), w)
				} else {
					dialog.ShowInformation("Success", "Multiface 128 enabled", w)
				}
			}),
			fyne.NewMenuItem("Enable Multiface 3", func() {
				if err := emu.peripherals.EnableMultiface(multiface.Multiface3, "roms"); err != nil {
					dialog.ShowError(fmt.Errorf("failed to enable Multiface 3: %w", err), w)
				} else {
					dialog.ShowInformation("Success", "Multiface 3 enabled", w)
				}
			}),
			fyne.NewMenuItem("Disable Multiface", func() {
				emu.peripherals.DisableMultiface()
				dialog.ShowInformation("Success", "Multiface disabled", w)
			}),
		),
		fyne.NewMenu("Emulator",
			fyne.NewMenuItem("Reboot", emu.reboot),
			fyne.NewMenuItem("Pause/Resume", emu.togglePause),
			fyne.NewMenuItem("Debugger", func() {
				dbg := debugger.New(emu.cpu, emu.mem, a)
				dbg.SetCallbacks(
					func() { emu.paused = true },              // Pause
					func() { emu.cpu.StepInstruction() },       // Step one instruction (no interrupt)
					func() { emu.paused = false },              // Run
					func() bool { return emu.paused },          // isPaused
				)
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
			fyne.NewMenuItem("Trigger NMI (F12)", func() {
				emu.kbd.SimulateNMI()
			}),
		),
	)
	w.SetMainMenu(mainMenu)

	// Layer the keyboard widget behind the screen so it can receive focus
	content := container.NewStack(keyboardWidget, screen)
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
