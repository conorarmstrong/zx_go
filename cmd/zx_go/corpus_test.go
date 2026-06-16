package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// Compatibility corpus: load real software headless, run it to a fixed frame,
// and assert it actually came up (a varied game screen, not a blank/BASIC/
// "R Tape loading error" screen) and optionally matches a recorded screen
// hash. This is the regression layer the unit tests can't provide — it catches
// "a real game silently stopped loading" (e.g. the Renegade-128K tape bug),
// which every mechanism-level test happily passed through.
//
// Game files are copyrighted and NOT committed: point the corpus at a folder
// via ZX_GO_CORPUS (defaults to ../../games relative to this package). Titles
// whose file is absent are skipped, so CI — which has no games — is green while
// anyone with the files gets the regression guard.
//
// Recording goldens: run with ZX_GO_CORPUS_UPDATE=1 to print each title's
// screen hash instead of asserting, then paste it into the table.

type corpusKey struct {
	frame      int
	row        int
	mask       byte
	holdFrames int
}

type corpusTitle struct {
	name   string
	file   string // relative to the corpus root
	model  roms.SpectrumModel
	load   string // "tap" | "tzx" | "snapshot"
	keys   []corpusKey
	frames int
	// wantMD5, if set, is the expected screen hash at the final frame. Empty
	// means "just assert the screen is non-trivial" (it loaded *something*).
	wantMD5 string
}

// enterAt presses ENTER (128 menu "Tape Loader" / BASIC) at the given frame.
func enterAt(frame int) corpusKey { return corpusKey{frame: frame, row: 6, mask: 0x01, holdFrames: 6} }

var corpusTitles = []corpusTitle{
	{
		// The exact bug this corpus exists to prevent regressing: a 128K
		// custom-loader tape that broke with "R Tape loading error".
		name:    "Renegade (128K, tape)",
		file:    "Renegade 128.tap",
		model:   roms.Model128K,
		load:    "tap",
		keys:    []corpusKey{enterAt(200)},
		frames:  40050,                              // full real-time load (~37k) + settle to the static menu
		wantMD5: "9ce6b84c403830fdcaec987ed645b0e4", // RENEGADE 128 options menu
	},
}

func corpusRoot() string {
	if r := os.Getenv("ZX_GO_CORPUS"); r != "" {
		return r
	}
	return filepath.Join("..", "..", "games")
}

func TestCompatibilityCorpus(t *testing.T) {
	root := corpusRoot()
	update := os.Getenv("ZX_GO_CORPUS_UPDATE") != ""
	ran := 0
	for _, ct := range corpusTitles {
		t.Run(ct.name, func(t *testing.T) {
			path := filepath.Join(root, ct.file)
			if _, err := os.Stat(path); err != nil {
				t.Skipf("not present (%s); set ZX_GO_CORPUS to a folder containing it", ct.file)
			}
			ran++
			emu, err := newEmulator(ct.model)
			if err != nil {
				t.Fatalf("newEmulator(%s): %v", roms.GetModelName(ct.model), err)
			}
			if err := loadCorpusTitle(emu, ct, path); err != nil {
				t.Fatalf("load %s: %v", ct.file, err)
			}
			runCorpusTitle(emu, ct)

			// Sample a settle window rather than one fixed frame: a loaded
			// title screen can vary by the odd frame (cursor, a transient), so
			// the robust assertion is "the expected screen APPEARS in the
			// window" — a correct load reaches it, a regression never does.
			const settle = 96
			hashes := make(map[string]int)
			var lastImg = emu.renderFrame()
			for i := 0; i < settle; i++ {
				runOneFrameHeadless(emu, ct.model)
				lastImg = emu.renderFrame()
				sum := md5.Sum(lastImg.Pix)
				hashes[hex.EncodeToString(sum[:])]++
			}
			if update {
				t.Logf("%s: %d distinct screen hashes over %d settle frames:", ct.name, len(hashes), settle)
				for h, c := range hashes {
					t.Logf("    %s x%d", h, c)
				}
				return
			}
			// Weak backstop: a single flat colour means nothing rendered. Text
			// menus are valid with few colours, so the golden hash does the
			// real "loaded correctly" work.
			if dp := distinctPixels(lastImg.Pix); dp < 2 {
				t.Errorf("%s: screen is a single flat colour — nothing rendered", ct.name)
			}
			if ct.wantMD5 != "" {
				if hashes[ct.wantMD5] == 0 {
					t.Errorf("%s: expected screen %s never appeared in %d settle frames — did not load correctly. Saw: %v",
						ct.name, ct.wantMD5, settle, hashes)
				}
			} else {
				t.Logf("%s: loaded; screen hashes over settle window = %v (set wantMD5 to lock one)", ct.name, hashes)
			}
		})
	}
	if ran == 0 {
		t.Skip("no corpus titles present — set ZX_GO_CORPUS to a folder with game files")
	}
}

func loadCorpusTitle(emu *emulator, ct corpusTitle, path string) error {
	switch ct.load {
	case "tap", "tzx":
		tp := ula.NewTapePlayer()
		var err error
		if ct.load == "tap" {
			err = tp.LoadTAP(path)
		} else {
			err = tp.LoadTZX(path)
		}
		if err != nil {
			return err
		}
		emu.ula.SetTapePlayer(tp)
		tp.Play()
		installTapeTrap(emu)
		return nil
	case "snapshot":
		snap := snapshot.New()
		if err := snap.Load(path); err != nil {
			return err
		}
		return applySnapshotToEmulator(emu, snap)
	default:
		return fmt.Errorf("unknown load type %q", ct.load)
	}
}

func runCorpusTitle(emu *emulator, ct corpusTitle) {
	for i := 0; i < ct.frames; i++ {
		runOneFrameHeadless(emu, ct.model)
		for _, k := range ct.keys {
			switch i {
			case k.frame:
				emu.kbd.PressMatrixKey(k.row, k.mask, true)
			case k.frame + k.holdFrames:
				emu.kbd.PressMatrixKey(k.row, k.mask, false)
			}
		}
	}
}

// distinctPixels counts distinct 32-bit RGBA values in a framebuffer — a cheap,
// rendering-tweak-tolerant proxy for "the screen has real content". A blank,
// BASIC, or "R Tape loading error" screen has only a handful of colours.
func distinctPixels(pix []byte) int {
	seen := make(map[uint32]struct{}, 64)
	for i := 0; i+3 < len(pix); i += 4 {
		v := uint32(pix[i])<<24 | uint32(pix[i+1])<<16 | uint32(pix[i+2])<<8 | uint32(pix[i+3])
		seen[v] = struct{}{}
		if len(seen) >= 256 {
			break
		}
	}
	return len(seen)
}
