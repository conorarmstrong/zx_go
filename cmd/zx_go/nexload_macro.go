package main

import (
	"fmt"
	"log/slog"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
)

// nextMenuLoopPC is the PC the NextZXOS ROM spins at while it waits for a key
// at the welcome screen and at the main menu — the signal that the boot has
// reached an interactive prompt.
const nextMenuLoopPC = 0x0c90

// nexKeyMatrix maps the characters needed to type a NextZXOS command line onto
// Spectrum keyboard-matrix (row, mask) presses. Symbols use SYMBOL SHIFT
// (row 7, 0x02) plus the symbol's key. Paths are typed lowercase — the SD card
// is FAT (case-insensitive) so this still matches mixed-case folder names.
var nexKeyMatrix = func() map[rune][][2]int {
	sym := [2]int{7, 0x02} // SYMBOL SHIFT
	letters := map[rune][2]int{
		'a': {1, 0x01}, 'b': {7, 0x10}, 'c': {0, 0x08}, 'd': {1, 0x04}, 'e': {2, 0x04},
		'f': {1, 0x08}, 'g': {1, 0x10}, 'h': {6, 0x10}, 'i': {5, 0x04}, 'j': {6, 0x08},
		'k': {6, 0x04}, 'l': {6, 0x02}, 'm': {7, 0x04}, 'n': {7, 0x08}, 'o': {5, 0x02},
		'p': {5, 0x01}, 'q': {2, 0x01}, 'r': {2, 0x08}, 's': {1, 0x02}, 't': {2, 0x10},
		'u': {5, 0x08}, 'v': {0, 0x10}, 'w': {2, 0x02}, 'x': {0, 0x04}, 'y': {5, 0x10},
		'z': {0, 0x02},
		'0': {4, 0x01}, '1': {3, 0x01}, '2': {3, 0x02}, '3': {3, 0x04}, '4': {3, 0x08},
		'5': {3, 0x10}, '6': {4, 0x10}, '7': {4, 0x08}, '8': {4, 0x04}, '9': {4, 0x02},
	}
	m := map[rune][][2]int{
		' ':  {{7, 0x01}},      // SPACE
		'.':  {sym, {7, 0x04}}, // SYMBOL SHIFT + M
		'/':  {sym, {0, 0x10}}, // SYMBOL SHIFT + V
		'-':  {sym, {6, 0x08}}, // SYMBOL SHIFT + J
		'\'': {sym, {4, 0x08}}, // SYMBOL SHIFT + 7
		'"':  {sym, {5, 0x01}}, // SYMBOL SHIFT + P
		':':  {sym, {0, 0x02}}, // SYMBOL SHIFT + Z
		',':  {sym, {7, 0x08}}, // SYMBOL SHIFT + N
	}
	for r, k := range letters {
		m[r] = [][2]int{k}
	}
	return m
}()

// macroStep is one stage of the NEXLOAD macro. Keys are held for the whole
// step (released and re-pressed on entry to each step); frames is how many
// emulated frames the step lasts. A step with waitMenu set instead runs until
// the CPU reaches the NextZXOS menu wait loop (or a safety timeout), which is
// how the boot phase waits for an interactive prompt.
type macroStep struct {
	keys     [][2]int
	frames   int
	waitMenu bool
}

// nexloadMacro drives the genuine NextZXOS `.nexload` dot command from the GUI
// run loop, one step per frame: it reaches the menu, opens the Command Line,
// types `.nexload <sdPath>`, and runs it. This is the faithful way to load a
// .nex — NextZXOS sets up the OS environment the game expects, so games that
// depend on the runtime (and would crash under bank-injection) work exactly as
// on hardware. Built only after the .nex is present on the SD card.
type nexloadMacro struct {
	steps []macroStep
	idx   int
	frame int
	keyOn bool
}

// newNexloadMacro builds the macro that loads sdPath (an absolute SD-card path,
// e.g. "/games/Next/Sonic/sonic.nex"). Timings mirror the verified headless
// sequence. The cmd is typed lowercase; spaces are typed literally (NEXLOAD
// takes the rest of the line as the filename, so no quoting is needed).
func newNexloadMacro(sdPath string) *nexloadMacro {
	var steps []macroStep
	hold := func(keys [][2]int, frames int) { steps = append(steps, macroStep{keys: keys, frames: frames}) }
	wait := func(frames int) { steps = append(steps, macroStep{frames: frames}) }

	steps = append(steps, macroStep{waitMenu: true}) // boot to the welcome screen
	hold([][2]int{{7, 0x01}}, 40)                    // SPACE -> "Start NextZXOS"
	wait(140)                                        // settle on the main menu
	hold([][2]int{{0, 0x01}, {4, 0x10}}, 6)          // cursor DOWN -> Command Line
	wait(10)
	hold([][2]int{{6, 0x01}}, 6) // ENTER -> command prompt
	wait(92)
	for _, c := range ".nexload " + strings.ToLower(sdPath) {
		if keys, ok := nexKeyMatrix[c]; ok {
			hold(keys, 4)
			wait(10)
		}
	}
	wait(15)
	hold([][2]int{{6, 0x01}}, 6) // ENTER -> run NEXLOAD
	wait(1500)                   // let the OS load and start the game

	return &nexloadMacro{steps: steps}
}

// tick advances the macro by one frame. It must be called once per executed
// frame, after the frame runs, so keys pressed here are seen by the next
// frame's keyboard scan. Returns true when the macro is finished (the caller
// should then drop it).
func (m *nexloadMacro) tick(e *emulator) bool {
	if m.idx >= len(m.steps) {
		m.releaseAll(e)
		return true
	}
	s := &m.steps[m.idx]
	if m.frame == 0 {
		// Entering a step: drop any previously-held keys, then press
		// this step's keys (if any).
		m.releaseAll(e)
		for _, k := range s.keys {
			e.kbd.PressMatrixKey(k[0], byte(k[1]), true)
		}
		m.keyOn = len(s.keys) > 0
	}
	m.frame++
	if s.waitMenu {
		// Safety timeout so a failed/absent boot can't wedge the macro.
		if e.cpu.PC == nextMenuLoopPC || m.frame > 900 {
			m.idx++
			m.frame = 0
		}
		return false
	}
	if m.frame >= s.frames {
		m.idx++
		m.frame = 0
	}
	return false
}

// confirmImportNex asks the user to confirm copying a .nex onto the SD card
// (required so NextZXOS's own loader can run it), then — on accept — imports
// and launches it. fileName is the base name; data is the file's bytes.
func (e *emulator) confirmImportNex(fileName string, data []byte) {
	if e.window == nil {
		// No window (headless): import directly.
		go e.importAndRunNex(fileName, data)
		return
	}
	msg := fmt.Sprintf(
		"To load %q the way a real Spectrum Next does — through NextZXOS's own loader, so games that depend on the operating system work correctly — a copy must be written to your SD card (under /imported/).\n\n"+
			"Your original file is left untouched. The machine will reset to load it.\n\n"+
			"Copy to the SD card and load now?",
		fileName)
	dialog.NewConfirm("Load .nex via NextZXOS", msg, func(ok bool) {
		if ok {
			go e.importAndRunNex(fileName, data)
		}
	}, e.window).Show()
}

// importAndRunNex copies data onto the SD card and starts the loader macro.
// It runs on its own goroutine (the SD write can be large) with the emulator
// paused so the in-memory image isn't modified mid-read.
func (e *emulator) importAndRunNex(fileName string, data []byte) {
	if e.sdImageSrc == nil {
		return
	}
	e.paused.Store(true)
	sdPath, err := sdcard.AddFileToFAT32(e.sdImageSrc.Bytes(), "imported", fileName, data)
	if err != nil {
		e.paused.Store(false)
		slog.Error("nex import: copy to SD card failed", "file", fileName, "err", err)
		if e.window != nil {
			fyne.Do(func() { dialog.ShowError(fmt.Errorf("copy %q to SD card: %w", fileName, err), e.window) })
		}
		return
	}
	// Persist so the imported game survives restarts (race-free while paused).
	if e.sdImagePath != "" {
		if err := e.sdImageSrc.WriteBackTo(e.sdImagePath); err != nil {
			slog.Warn("nex import: persisting to the SD image failed", "err", err)
		}
	}
	slog.Info("nex import: loading via NextZXOS", "file", fileName, "sdPath", sdPath)
	e.startNexloadMacro(sdPath)
}

// startNexloadMacro reboots into a clean NextZXOS and begins driving the
// .nexload command to load the .nex at sdPath (an absolute SD-card path) via
// the genuine OS loader. The reboot guarantees a fresh OS state regardless of
// what was running before.
func (e *emulator) startNexloadMacro(sdPath string) {
	e.reboot()
	// Arm the macro before releasing pause: the emulation goroutine's
	// run loop checks e.nexloadMacro on every frame it executes, so
	// setting it first avoids a window where an unpaused frame runs
	// with no macro driving it yet.
	e.nexloadMacro = newNexloadMacro(sdPath)
	e.paused.Store(false)
}

// releaseAll clears every key the macro might be holding.
func (m *nexloadMacro) releaseAll(e *emulator) {
	if !m.keyOn {
		return
	}
	for row := 0; row < 8; row++ {
		e.kbd.PressMatrixKey(row, 0xFF, false)
	}
	m.keyOn = false
}
