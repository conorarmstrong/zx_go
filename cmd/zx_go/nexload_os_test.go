package main

import (
	"bytes"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// This file proves the faithful path for loading .nex games that depend on
// the NextZXOS runtime: instead of bank-injecting (which overwrites the OS's
// own workspace banks and breaks the game's RST 8 file I/O), it drives the
// genuine NextZXOS ".nexload" dot command from the Command Line — exactly how
// a real Spectrum Next loads these games. The OS CDs into the game's folder
// and provides the environment, so the game's runtime file opens succeed.

// nexRunFrames runs the machine GUI-style for n frames.
func nexRunFrames(emu *emulator, n int) {
	for i := 0; i < n; i++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
	}
}

// nexPressCombo presses a set of matrix keys together, holds, releases, then
// pauses — long enough for the NextZXOS 50 Hz key scan to register a distinct
// keystroke between presses.
func nexPressCombo(emu *emulator, keys [][2]int, hold, gap int) {
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), true)
	}
	nexRunFrames(emu, hold)
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), false)
	}
	nexRunFrames(emu, gap)
}

// nexTypeLine types an ASCII string onto the NextZXOS command line.
func nexTypeLine(emu *emulator, s string) {
	for _, c := range s {
		if keys, ok := nexKeyMatrix[c]; ok {
			nexPressCombo(emu, keys, 4, 10)
		}
	}
}

// nexloadFromMenu drives the real NextZXOS NEXLOAD on sdPath. It expects the
// machine at the main menu with the cursor on "Browser" (as left by
// bootNextToMenu), steps down to "Command Line", changes to the game's own
// directory, types `.nexload <file>`, presses ENTER, then runs loadFrames
// frames to let the OS load and start the game. The path is typed lowercase;
// spaces are typed literally (NEXLOAD takes the rest of the line as the
// filename, so no quoting is needed).
//
// The CD matters, exactly as it does for the NextBASIC harness. A game that
// loads assets at runtime opens them by a path relative to the current
// directory, and a user reaches a game through the Browser, which changes
// into its folder first. Launching by absolute path from the root left the
// current directory at "/", so every relative open failed: Eternal Battle
// built its IM 2 table, got nothing back, and ran off into its own data until
// it hit an FF byte and RST 38'd into a filler ramp. That looked like a
// hardware bug and is not one.
// nexWaitPrompt runs until the display has stopped changing, which is how the
// command prompt signals it has finished the previous line and is ready for
// the next one.
//
// Waiting a fixed number of frames here was a race: too short and the next
// typed line lost characters, and whether it was long enough depended on what
// else had run in the same process, so the suite passed or failed on test
// ordering. Waiting on the main-menu loop PC did not work either, because the
// command prompt never reaches it. Screen stability is the thing actually
// being waited for, so wait on that.
func nexWaitPrompt(emu *emulator, maxFrames int) {
	prev, _ := nextScreen(emu)
	stable := 0
	for i := 0; i < maxFrames; i++ {
		nexRunFrames(emu, 5)
		h, _ := nextScreen(emu)
		if h == prev {
			if stable++; stable >= 6 { // ~30 frames unchanged
				return
			}
		} else {
			stable = 0
			prev = h
		}
	}
}

func nexloadFromMenu(emu *emulator, sdPath string, loadFrames int) {
	nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 10) // cursor DOWN -> Command Line
	nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)            // ENTER -> the command prompt
	nexRunFrames(emu, 200)

	dir, file := path.Split(sdPath)
	dir = strings.TrimSuffix(dir, "/")
	if dir != "" {
		// Quoted because .cd splits its argument on spaces.
		nexTypeLine(emu, `.cd "`+strings.ToLower(dir)+`"`)
		nexRunFrames(emu, 25)
		nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)
		// Wait for the command prompt to come back rather than guessing a
		// frame count. A fixed wait here was a race: too short and the
		// next line lost characters, and whether it was long enough
		// depended on what had run before in the same process.
		nexWaitPrompt(emu, 200)
	}

	nexTypeLine(emu, ".nexload "+strings.ToLower(file))
	nexRunFrames(emu, 15)
	nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12) // ENTER -> run NEXLOAD
	nexRunFrames(emu, loadFrames)
}

// TestNexloadOSGamesIfPresent verifies that games which depend on the NextZXOS
// runtime (they open data files via RST 8 at startup) load and render when
// driven through the real `.nexload` loader — the bank-injection path can't
// host them because it clobbers the OS's workspace banks. Skipped when the
// Next ROMs / SD games (gitignored) are absent, so CI stays green.
func TestNexloadOSGamesIfPresent(t *testing.T) {
	cases := []struct {
		hostPath, sdPath, name string
		loadFrames             int
	}{
		{
			"../../roms/next/sd/games/Next/Warhawk/Warhawk.nex",
			"/games/Next/Warhawk/Warhawk.nex", "warhawk", 1400,
		},
		{
			"../../roms/next/sd/games/Next/Revival Survival/RevivalSurvival.nex",
			"/games/Next/Revival Survival/RevivalSurvival.nex", "revival", 1400,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.hostPath); err != nil {
				t.Skipf("%s not present (gitignored SD content)", c.hostPath)
			}
			emu := bootNextToMenu(t)
			nexloadFromMenu(emu, c.sdPath, c.loadFrames)

			img := emu.renderFrame()
			nonBlank := !uniformImage(img)
			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					_ = os.WriteFile(dir+"/"+c.name+"-nexload.png", buf.Bytes(), 0o644)
				}
			}
			if emu.cpu.PC == nextMenuLoopPC {
				t.Errorf("%s: NEXLOAD returned to the menu (PC=%#04x) — game did not launch", c.name, emu.cpu.PC)
			}
			if !nonBlank {
				t.Errorf("%s: screen blank after NEXLOAD — game did not render", c.name)
			}
			t.Logf("%s: launched via NEXLOAD, PC=%#04x nonblank=%v", c.name, emu.cpu.PC, nonBlank)
		})
	}
}
