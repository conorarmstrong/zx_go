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
// nexMacroHoldFrames bounds how long one character is held waiting for the OS
// to echo it. Generous on purpose: expiry means a dropped keystroke and a
// corrupted command line, and unlike the test typist this engine cannot
// re-offer the key.
const nexMacroHoldFrames = 200

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

// nexCmdEcho returns the command line as NextZXOS itself holds it. The OS keeps
// the line being edited in RAM bank 7 as plain ASCII, NUL padded: the first 32
// columns at $0000 and the continuation row at $0028. That layout is empirical
// — dumped by typing known strings — but it needs no trusting: a layout change
// makes the very first character typed fail to confirm.
//
// It is what makes typing at the command line honest. The OS finishes a command
// and only then clears the line, and while it is busy it takes keys from the ROM
// and throws them away, so a keystroke sent on a frame count can vanish without
// trace. Reading the OS's own copy of the line says whether a character actually
// arrived.
func nexCmdEcho(e *emulator) string {
	p := e.mem.GetPage(7)
	if len(p) < 0x43 {
		return ""
	}
	var b []byte
	for _, r := range [2][2]int{{0x00, 0x20}, {0x28, 0x43}} {
		for i := r[0]; i < r[1]; i++ {
			if c := p[i]; c >= 0x20 && c < 0x7f {
				b = append(b, c)
			}
		}
	}
	return string(b)
}

// macroStep is one stage of the NEXLOAD macro. Keys are held for the whole
// step (released and re-pressed on entry to each step); frames is how many
// emulated frames the step lasts. A step with until set ends as soon as until
// reports true — it is waiting on something the guest did, and frames is only
// its safety bound. A step with waitMenu set instead runs until the CPU reaches
// the NextZXOS menu wait loop (or a safety timeout), which is how the boot
// phase waits for an interactive prompt.
type macroStep struct {
	keys     [][2]int
	frames   int
	waitMenu bool
	until    func(e *emulator) bool
}

// nexMacroSettled returns a per-step predicate that reports true once the guest
// has completed scans keyboard scans, counted as frames in which its port-$FE
// read count advanced. It is the guest's clock, not the host's, so the gap
// between keystrokes stays right however busy the machine is — and it is what
// stops the ROM's key re-arm delivering a letter twice when the next character
// is a SYMBOL SHIFT combo on the same key ("m" then ".", which is SYMBOL
// SHIFT + M).
func nexMacroSettled(scans int) func(e *emulator) bool {
	var prev uint64
	var started bool
	done := 0
	return func(e *emulator) bool {
		if e.ula == nil {
			return true
		}
		n := e.ula.FEReadCount()
		if started && n != prev {
			done++
		}
		prev, started = n, true
		return done >= scans
	}
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
	holdUntil := func(keys [][2]int, timeout int, until func(*emulator) bool) {
		steps = append(steps, macroStep{keys: keys, frames: timeout, until: until})
	}
	waitUntil := func(timeout int, until func(*emulator) bool) {
		steps = append(steps, macroStep{frames: timeout, until: until})
	}

	steps = append(steps, macroStep{waitMenu: true}) // boot to the welcome screen
	hold([][2]int{{7, 0x01}}, 40)                    // SPACE -> "Start NextZXOS"
	wait(140)                                        // settle on the main menu
	hold([][2]int{{0, 0x01}, {4, 0x10}}, 6)          // cursor DOWN -> Command Line
	wait(10)
	hold([][2]int{{6, 0x01}}, 6) // ENTER -> command prompt
	// Wait for the prompt itself, not for a frame count: the menu leaves its
	// own data in the bank-7 command-line area, so an empty line is the
	// prompt's own signal that it has started and is ready to be typed at.
	waitUntil(300, func(e *emulator) bool { return nexCmdEcho(e) == "" })

	// Type the command, confirming each character against the OS's copy of
	// the line before moving on. A fixed hold-and-wait per key was a race:
	// whenever the OS was still busy the keystroke was taken by the ROM and
	// discarded, and the whole line came out wrong.
	line := ".nexload " + strings.ToLower(sdPath)
	typed := ""
	for _, c := range line {
		keys, ok := nexKeyMatrix[c]
		if !ok {
			continue
		}
		typed += string(c)
		want := typed
		// Hold the character until the OS's own copy of the line shows it.
		//
		// The bound is a safety net, not a retry: when it expires the step
		// simply advances. That is why it is generous. A keystroke dropped
		// because NextZXOS was busy leaves a line missing a character
		// (".nexlad /games/…"), NEXLOAD reports no such file, and the GUI
		// drops back to the prompt with nothing on screen to explain it.
		//
		// It cannot be turned into a re-offer here the way the test typist
		// does: these calls append macro STEPS, so a loop would press the key
		// a fixed number of times whatever happened and duplicate characters.
		// Making expiry loud is the honest half-measure — the user gets a
		// diagnostic instead of a silent wrong filename.
		holdUntil(keys, nexMacroHoldFrames, func(e *emulator) bool {
			return nexCmdEcho(e) == want
		})
		waitUntil(20, nexMacroSettled(6))
		waitUntil(nexMacroHoldFrames, func(e *emulator) bool {
			return nexCmdEcho(e) == want
		})
	}
	// One last check before ENTER: if the line is not what was asked for,
	// say so rather than running a mangled command.
	waitUntil(1, func(e *emulator) bool {
		if got := nexCmdEcho(e); got != line {
			slog.Warn("nexload macro: command line does not match what was typed",
				"want", line, "got", got,
				"note", "a keystroke was dropped while NextZXOS was busy; NEXLOAD will fail")
		}
		return true
	})

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
	if s.until != nil && s.until(e) {
		m.idx++
		m.frame = 0
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
