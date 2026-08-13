package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// runBasicFromMenu launches a NextBASIC program the way a user would: open the
// Command Line, change to the program's directory, LOAD it, then RUN.
//
// The CD matters. NextBASIC's LOAD resolves against the current directory, so
// `LOAD "/full/path/prog.bas"` fails with "File not found" — verified — while
// `.cd <dir>` followed by `LOAD "prog.bas"` works.
// nexTypeLineBlind types an ASCII string without confirming each character
// against the OS, unmapped characters silently skipped.
//
// The NEXLOAD harness uses nexTypeLine, which confirms every character against
// the OS's own copy of the command line and is immune to the OS being busy.
// That cannot be used here: NextBASIC's LOAD leaves the previous line in the
// bank-7 mirror instead of clearing it, and a running program never touches it
// at all, so there is nothing to confirm against once LOAD has been submitted.
// This suite therefore keeps the timing-based typist it was written against.
func nexTypeLineBlind(emu *emulator, s string) {
	for _, c := range s {
		if keys, ok := nexKeyMatrix[c]; ok {
			nexPressCombo(emu, keys, 4, 10)
		}
	}
}

func runBasicFromMenu(emu *emulator, sdDir, file string, runFrames int) {
	enter := func(frames int) {
		nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)
		nexRunFrames(emu, frames)
	}
	nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 10) // DOWN -> Command Line
	enter(80)

	// The path is quoted because .cd splits its argument on spaces: an
	// unquoted "/games/next/nextbasic invaders" is read as two paths and both
	// are reported missing, which is why NextBASIC Invaders would not load.
	nexTypeLineBlind(emu, `.cd "`+strings.ToLower(sdDir)+`"`)
	nexRunFrames(emu, 10)
	enter(120)

	nexTypeLineBlind(emu, `load "`+strings.ToLower(file)+`"`)
	nexRunFrames(emu, 15)
	enter(400)

	// Some programs autostart on LOAD; a RUN afterwards is harmless for those
	// and necessary for the rest.
	nexTypeLineBlind(emu, "run")
	nexRunFrames(emu, 15)
	enter(runFrames)
}

// TestNextBasicSDGames screens the NextBASIC (.bas) games on the SD card.
//
// These cannot be reached by NEXLOAD, which loads .nex files, so without this
// they were simply unscreened — including NextBASIC Invaders, which the
// compatibility manifest still lists as a Known issue.
func TestNextBasicSDGames(t *testing.T) {
	const sdRoot = "../../roms/next/sd"
	gamesDir := filepath.Join(sdRoot, "games", "Next")
	if _, err := os.Stat(gamesDir); err != nil {
		t.Skipf("no Next SD games present (gitignored): %v", err)
	}

	var basFiles []string
	_ = filepath.Walk(gamesDir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.EqualFold(filepath.Ext(p), ".bas") {
			basFiles = append(basFiles, p)
		}
		return nil
	})
	sort.Strings(basFiles)
	if len(basFiles) == 0 {
		t.Skip("no .bas games on the SD card")
	}

	for _, host := range basFiles {
		rel, err := filepath.Rel(sdRoot, filepath.Dir(host))
		if err != nil {
			continue
		}
		sdDir := "/" + filepath.ToSlash(rel)
		name := filepath.Base(filepath.Dir(host))

		t.Run(name, func(t *testing.T) {
			emu := bootNextToMenu(t)
			runBasicFromMenu(emu, sdDir, filepath.Base(host), 1200)

			px, cols := drawnInDisplayWindow(emu)
			nonBlank := px >= 1000 && cols >= 2
			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					safe := strings.ReplaceAll(name, " ", "_")
					_ = os.WriteFile(filepath.Join(dir, "bas-"+safe+".png"), buf.Bytes(), 0o644)
				}
			}
			verdict := "Renders"
			if !nonBlank {
				verdict = "Blank"
			}
			t.Logf("BASVERDICT %-10s %-22s PC=%#04x pixels=%d colours=%d", verdict, name, emu.cpu.PC, px, cols)
		})
	}
}

// drawnInDisplayWindow counts pixels differing from the frame's dominant
// colour, measured inside the 256x192 display window with the border cropped
// off, and the number of distinct colours there.
//
// uniformImage is too weak to judge a launch: a bare BASIC prompt with a
// single flashing cursor block is "not uniform" and passes it, which reported
// NextBASIC Invaders as rendering when it had not run at all.
func drawnInDisplayWindow(emu *emulator) (pixels, colours int) {
	img := emu.renderFrame()
	const bl, bt, w, h = 32, 24, 256, 192
	hist := map[uint32]int{}
	for y := bt; y < bt+h; y++ {
		for x := bl; x < bl+w; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			hist[(r>>8)<<24|(g>>8)<<16|(b>>8)<<8|(a>>8)]++
		}
	}
	best := 0
	for _, n := range hist {
		if n > best {
			best = n
		}
	}
	return w*h - best, len(hist)
}
