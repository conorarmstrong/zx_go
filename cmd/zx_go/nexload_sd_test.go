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

	for _, host := range nexFiles {
		rel, err := filepath.Rel(sdRoot, host)
		if err != nil {
			continue
		}
		sdPath := "/" + filepath.ToSlash(rel)
		name := filepath.Base(filepath.Dir(host))

		t.Run(name, func(t *testing.T) {
			emu := bootNextToMenu(t)
			nexloadFromMenu(emu, sdPath, 1400)

			img := emu.renderFrame()
			nonBlank := !uniformImage(img)
			launched := emu.cpu.PC != nextMenuLoopPC

			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					safe := strings.ReplaceAll(name, " ", "_")
					_ = os.WriteFile(filepath.Join(dir, safe+".png"), buf.Bytes(), 0o644)
				}
			}

			verdict := "Renders"
			switch {
			case !launched:
				verdict = "DidNotLaunch"
			case !nonBlank:
				verdict = "Blank"
			}
			t.Logf("SDVERDICT %-14s %-28s PC=%#04x nonblank=%v", verdict, name, emu.cpu.PC, nonBlank)

			if !launched {
				t.Errorf("%s: NEXLOAD returned to the menu — the game did not launch", name)
			}
		})
	}
}
