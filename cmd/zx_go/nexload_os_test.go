package main

import (
	"bytes"
	"os"
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
// bootNextToMenu), steps down to "Command Line", types `.nexload <path>`,
// presses ENTER, then runs loadFrames frames to let the OS load and start the
// game. The path is typed lowercase; spaces are typed literally (NEXLOAD takes
// the rest of the line as the filename, so no quoting is needed).
func nexloadFromMenu(emu *emulator, sdPath string, loadFrames int) {
	nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 10) // cursor DOWN -> Command Line
	nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)            // ENTER -> the command prompt
	nexRunFrames(emu, 80)
	nexTypeLine(emu, ".nexload "+strings.ToLower(sdPath))
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
