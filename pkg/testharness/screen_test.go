package testharness

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestScreenTextReadsBootBanner boots the 48K Spectrum and waits
// for the copyright banner to appear in screen text. The 48K boot
// sequence prints "© 1982 Sinclair Research Ltd" near the bottom
// of the screen — we wait for the literal string "1982" to show up.
//
// This is the canonical end-to-end smoke test for the harness +
// OCR pipeline: if it passes, the harness can boot real ROM code,
// run it for the right number of frames, and read the resulting
// text out of screen memory correctly.
func TestScreenTextReadsBootBanner(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	frames, err := h.RunUntilText("1982", 200)
	if err != nil {
		t.Logf("screen at timeout:\n%s", h.ScreenText())
		t.Fatalf("RunUntilText: %v", err)
	}
	t.Logf("BASIC banner appeared after %d frames", frames)

	// Sanity check: verify the full Sinclair line is recognisable.
	text := h.ScreenText()
	if !strings.Contains(text, "Sinclair Research Ltd") {
		t.Errorf("expected 'Sinclair Research Ltd' in screen text, got:\n%s", text)
	}
}

// TestTypeReachesScreen boots BASIC, taps a few keys, and verifies
// they appear in screen memory. This validates the keyboard plumbing
// (Press/Release/TapKey → keyboard.Scan → IN port → BASIC reads it
// → BASIC echoes to screen → ScreenText reads it back).
func TestTypeReachesScreen(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Wait for the BASIC prompt to be ready (the © banner means
	// READY).
	if _, err := h.RunUntilText("1982", 200); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Type "P" — on 48K BASIC at the start of a line, "P" is the
	// PRINT keyword (single-key entry in K mode). It should appear
	// near the bottom of the screen.
	h.TapKey(fyne.KeyP)
	// Give BASIC time to process the keystroke and echo it.
	h.RunFrames(20)

	text := h.ScreenText()
	if !strings.Contains(text, "PRINT") {
		t.Errorf("expected 'PRINT' on screen after tapping P, got:\n%s", text)
	}
}

// TestMatchCharSpaceFastPath confirms an empty cell maps to space
// without walking the full character set. (Cosmetic — a regression
// here just means slow tests, not wrong tests.)
func TestMatchCharSpaceFastPath(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.RunFrames(1) // make sure ROM is loaded
	if got := h.matchChar([8]byte{}); got != ' ' {
		t.Errorf("matchChar(empty) = %q, want space", got)
	}
}
