package main

import (
	"image/png"
	"log/slog"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/sam"
)

// newSamEmulator builds an emulator running the MGT SAM Coupé (pkg/sam). The SAM
// has its own memory map, I/O/ASIC, keyboard and sound, so the real machine
// lives in e.sam and the run/render/input paths route there; e.mem/e.kbd hold
// inert Spectrum-typed stand-ins purely so the GUI's menu/config code (which
// reads them unconditionally) doesn't nil-panic. The bundled ROM 3.0 is used.
func newSamEmulator() (*emulator, error) {
	m, err := sam.NewDefault()
	if err != nil {
		return nil, err
	}
	standInMem, err := memory.New("roms", roms.Model48K)
	if err != nil {
		return nil, err
	}
	e := &emulator{
		cpu:          m.CPU,
		mem:          standInMem,
		kbd:          keyboard.New(),
		sam:          m,
		peripherals:  peripherals.NewPeripheralManager(standInMem, "roms"),
		model:        roms.ModelSAM,
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10),
		stopChan:     make(chan struct{}),
	}
	e.paused.Store(true)
	return e, nil
}

// samKeyMatrix maps a host key to a SAM matrix (row, column) position. Symbols
// reached via Shift on the host (e.g. AZERTY '.') need the typed-character layer
// (a follow-up, like the Spectrum's); this covers letters, digits, the editing
// keys, the two shifts and the cursor keys. See pkg/sam keyboard.go for the
// matrix layout.
var samKeyMatrix = map[fyne.KeyName][2]int{
	desktop.KeyShiftLeft:   {0, 0}, // SHIFT
	desktop.KeyShiftRight:  {7, 1}, // SYMBOL
	desktop.KeyControlLeft: {7, 1}, desktop.KeyControlRight: {7, 1},
	desktop.KeyAltLeft: {7, 1}, desktop.KeyAltRight: {7, 1},
	fyne.KeyZ: {0, 1}, fyne.KeyX: {0, 2}, fyne.KeyC: {0, 3}, fyne.KeyV: {0, 4},
	fyne.KeyA: {1, 0}, fyne.KeyS: {1, 1}, fyne.KeyD: {1, 2}, fyne.KeyF: {1, 3}, fyne.KeyG: {1, 4},
	fyne.KeyQ: {2, 0}, fyne.KeyW: {2, 1}, fyne.KeyE: {2, 2}, fyne.KeyR: {2, 3}, fyne.KeyT: {2, 4},
	fyne.Key1: {3, 0}, fyne.Key2: {3, 1}, fyne.Key3: {3, 2}, fyne.Key4: {3, 3}, fyne.Key5: {3, 4},
	fyne.Key0: {4, 0}, fyne.Key9: {4, 1}, fyne.Key8: {4, 2}, fyne.Key7: {4, 3}, fyne.Key6: {4, 4},
	fyne.KeyP: {5, 0}, fyne.KeyO: {5, 1}, fyne.KeyI: {5, 2}, fyne.KeyU: {5, 3}, fyne.KeyY: {5, 4},
	fyne.KeyReturn: {6, 0}, fyne.KeyL: {6, 1}, fyne.KeyK: {6, 2}, fyne.KeyJ: {6, 3}, fyne.KeyH: {6, 4},
	fyne.KeySpace: {7, 0}, fyne.KeyM: {7, 2}, fyne.KeyN: {7, 3}, fyne.KeyB: {7, 4},
	fyne.KeyEscape: {3, 5}, fyne.KeyTab: {3, 6},
	fyne.KeyMinus: {4, 5}, fyne.KeyEqual: {4, 6}, fyne.KeyBackspace: {4, 7}, // -, +, DELETE
	fyne.KeyUp: {8, 1}, fyne.KeyDown: {8, 2}, fyne.KeyLeft: {8, 3}, fyne.KeyRight: {8, 4},
}

// samLowerLetters maps Fyne's localized lowercase letter names to the matrix, so
// letters work on non-US layouts (the same trick the Spectrum keyboard uses).
var samLowerLetters = map[string][2]int{
	"z": {0, 1}, "x": {0, 2}, "c": {0, 3}, "v": {0, 4},
	"a": {1, 0}, "s": {1, 1}, "d": {1, 2}, "f": {1, 3}, "g": {1, 4},
	"q": {2, 0}, "w": {2, 1}, "e": {2, 2}, "r": {2, 3}, "t": {2, 4},
	"p": {5, 0}, "o": {5, 1}, "i": {5, 2}, "u": {5, 3}, "y": {5, 4},
	"l": {6, 1}, "k": {6, 2}, "j": {6, 3}, "h": {6, 4},
	"m": {7, 2}, "n": {7, 3}, "b": {7, 4},
}

// samSetKey presses/releases a host key on the SAM matrix.
func (e *emulator) samSetKey(name fyne.KeyName, pressed bool) {
	if rc, ok := samKeyMatrix[name]; ok {
		e.sam.Kbd.SetKey(rc[0], rc[1], pressed)
		return
	}
	if rc, ok := samLowerLetters[string(name)]; ok {
		e.sam.Kbd.SetKey(rc[0], rc[1], pressed)
	}
}

// runSAMHeadless boots the SAM Coupé and (optionally) renders a screenshot,
// using the same newSamEmulator + renderFrame path the GUI uses. It is the
// headless verification path for the SAM (skips the Spectrum instrumentation in
// runHeadless, which is bound to the Spectrum memory).
func runSAMHeadless(f *cliFlags) {
	emu, err := newSamEmulator()
	if err != nil {
		slog.Error("sam: emulator construction failed", "err", err)
		os.Exit(1)
	}
	frames := f.frames
	if frames == 0 {
		frames = 400 // enough for the (contended) boot to draw its screen
	}
	for i := 0; i < frames; i++ {
		emu.sam.RunFrame()
	}
	slog.Info("sam: ran headless", "frames", frames, "pc", emu.sam.CPU.PC, "mode", emu.sam.Mem.ScreenMode())
	if f.saveScreen != "" {
		out, err := os.Create(f.saveScreen)
		if err != nil {
			slog.Error("sam: cannot create screenshot", "err", err)
			os.Exit(1)
		}
		defer func() { _ = out.Close() }()
		if err := png.Encode(out, emu.renderFrame()); err != nil {
			slog.Error("sam: screenshot encode failed", "err", err)
			os.Exit(1)
		}
		slog.Info("sam: wrote screenshot", "path", f.saveScreen)
	}
}
