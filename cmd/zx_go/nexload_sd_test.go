package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNexloadSDGames screens every .nex game on the SD card through the
// genuine NextZXOS NEXLOAD dot command — the only path that can host a title
// which calls the OS at runtime, and therefore the only honest way to judge
// Next game compatibility.
//
// This is the Next half of the compatibility corpus. The classic titles
// screened by pkg/testharness say nothing about Next-only hardware (the
// zxnDMA, the Copper), so it is this suite, not that one, which can surface a
// program exercising them.
//
// The SD content is gitignored, so the test skips when it is absent and CI
// stays green. It is a measurement harness: it records a verdict per title and
// only fails when a game returns to the menu, which means NEXLOAD itself did
// not launch it.
func TestNexloadSDGames(t *testing.T) {
	const sdRoot = "../../roms/next/sd"
	gamesDir := filepath.Join(sdRoot, "games", "Next")
	if _, err := os.Stat(gamesDir); err != nil {
		t.Skipf("no Next SD games present (gitignored): %v", err)
	}

	var nexFiles []string
	_ = filepath.Walk(gamesDir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.EqualFold(filepath.Ext(p), ".nex") {
			nexFiles = append(nexFiles, p)
		}
		return nil
	})
	sort.Strings(nexFiles)
	if len(nexFiles) == 0 {
		t.Skip("no .nex games on the SD card")
	}
	// The bug this suite used to have was an ordering bug: each launch left
	// the machine in a slightly different state and the next one raced it.
	// Go's -shuffle does not reorder sub-tests, so NEX_SD_REVERSE=1 is the
	// hook for running the corpus backwards and proving order does not matter.
	if os.Getenv("NEX_SD_REVERSE") != "" {
		for i, j := 0, len(nexFiles)-1; i < j; i, j = i+1, j-1 {
			nexFiles[i], nexFiles[j] = nexFiles[j], nexFiles[i]
		}
	}

	for _, host := range nexFiles {
		rel, err := filepath.Rel(sdRoot, host)
		if err != nil {
			continue
		}
		sdPath := "/" + filepath.ToSlash(rel)
		name := filepath.Base(filepath.Dir(host))

		t.Run(name, func(t *testing.T) {
			// The launch is driven by typing into the real NextZXOS command
			// line one synthetic keystroke at a time — the faithful path.
			// Every character is confirmed against the OS's own copy of the
			// line, so a typing failure is reported as one, here, instead of
			// turning into a verdict about the game.
			emu := bootNextToMenu(t)
			launched, err := nexloadFromMenu(emu, sdPath, 1400)
			if err != nil {
				t.Fatalf("%s: driving the NextZXOS command line: %v", name, err)
			}

			img := emu.renderFrame()
			nonBlank := !uniformImage(img)
			// Sample the PC that goes with THIS verdict before nexAtOSKeyWait
			// runs the guest on: it advances 20 frames, so reporting
			// emu.cpu.PC afterwards described a machine 20 frames past the
			// state the verdict came from, and could contradict the verdict
			// it was printed beside.
			verdictPC := emu.cpu.PC
			backAtOS := nexAtOSKeyWait(emu)

			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					safe := strings.ReplaceAll(name, " ", "_")
					_ = os.WriteFile(filepath.Join(dir, safe+".png"), buf.Bytes(), 0o644)
				}
			}

			// The strict condition: the game must still be running its own
			// code at the end of the window. "It ran for a while and then
			// handed the machine back" is NOT a pass — a title that loads,
			// runs and dies would look identical to one that works, and
			// accepting it would blind this suite to that whole class of
			// regression across the corpus.
			//
			// This is only assertable because the guest clock is pinned in
			// bootNextToMenu. Without that the outcome varies run to run on
			// the same binary, and the temptation is to widen the verdict
			// until the variation fits inside it. Fix the nondeterminism
			// instead.
			verdict := "Renders"
			switch {
			case !launched:
				verdict = "DidNotLaunch"
			case backAtOS:
				verdict = "ReturnedToOS"
			case !nonBlank:
				verdict = "Blank"
			}
			t.Logf("SDVERDICT %-14s %-28s PC=%#04x nonblank=%v", verdict, name, verdictPC, nonBlank)

			if !launched {
				t.Errorf("%s: NEXLOAD never handed the machine over (PC=%#04x) — the game did not launch", name, verdictPC)
			}
			if backAtOS {
				t.Errorf("%s: back at the NextZXOS key-wait at the end of the window (PC=%#04x) — "+
					"the game launched and then gave the machine back", name, verdictPC)
			}
		})
	}
}
