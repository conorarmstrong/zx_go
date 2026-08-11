package testharness

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Screening a title means answering one question mechanically: did it get
// anywhere? The compatibility manifest has carried thirteen "Untested" entries
// because that judgement was a human sitting and watching. It does not have to
// be.
//
// Three signals separate the cases that matter, and all three come from the
// display the guest actually drew:
//
//	ink      how much of the 6144-byte bitmap is non-zero
//	attrs    how many distinct attribute values the 768-byte area holds
//	moved    whether the frame changed between two samples taken apart in time
//
// A title that loaded and is running animates. One that drew a title screen
// and is waiting for a key does not, but has drawn a lot. One that died leaves
// the screen blank or nearly so.

// TestClassifyBlank pins the failure case: nothing was drawn.
func TestClassifyBlank(t *testing.T) {
	for _, s := range []Screening{
		{Pixels: 0, Colours: 1, Moved: false},
		{Pixels: 0, Colours: 1, Moved: true},    // a flashing border is not content
		{Pixels: 400, Colours: 2, Moved: false}, // a couple of characters is not content
	} {
		if got := Classify(s); got != VerdictBlank {
			t.Errorf("Classify(%+v) = %v, want VerdictBlank", s, got)
		}
	}
}

// TestClassifyLive pins the success case: a lot drawn, and still changing.
func TestClassifyLive(t *testing.T) {
	for _, s := range []Screening{
		{Pixels: 30000, Colours: 17, Moved: true}, // Batty running, composited scale
		{Pixels: 9000, Colours: 8, Moved: true},
	} {
		if got := Classify(s); got != VerdictLive {
			t.Errorf("Classify(%+v) = %v, want VerdictLive", s, got)
		}
	}
}

// TestClassifyStatic pins the middle case, which is the interesting one: a
// title screen or menu waiting for input. Drawn, but not moving. That is a
// real result, not a failure, and it must not be reported as either.
func TestClassifyStatic(t *testing.T) {
	for _, s := range []Screening{
		{Pixels: 20000, Colours: 13, Moved: false}, // the Exolon menu, composited scale
		{Pixels: 9350, Colours: 8, Moved: false},
	} {
		if got := Classify(s); got != VerdictStatic {
			t.Errorf("Classify(%+v) = %v, want VerdictStatic", s, got)
		}
	}
}

// TestClassifyLoaderIsNotContent guards the trap that would make this whole
// exercise worthless: a tape loader draws vivid moving stripes for minutes.
// Judged on movement alone it looks exactly like a running game.
//
// Since measurement crops the border away, a loader now scores almost nothing:
// its stripes are entirely outside the display window, which stays empty until
// the title paints. So the realistic loader shape is "moving, but with nearly
// no content", and that is what must not read as Live.
func TestClassifyLoaderIsNotContent(t *testing.T) {
	loading := Screening{Pixels: 200, Colours: 2, Moved: true}
	if got := Classify(loading); got == VerdictLive {
		t.Errorf("a tape loader classified as %v; movement alone is not evidence a title ran", got)
	}
}

// TestClassifyErrorBeatsEverything pins precedence. A BASIC error report is
// definitive regardless of what is on screen behind it, so it wins over ink
// and movement.
func TestClassifyErrorBeatsEverything(t *testing.T) {
	s := Screening{Pixels: 30000, Colours: 15, Moved: true, Error: "Integer out of range, 0:1"}
	if got := Classify(s); got != VerdictError {
		t.Errorf("Classify(%+v) = %v, want VerdictError", s, got)
	}
}

// TestVerdictNamesAreStable pins the strings that end up in the manifest.
func TestVerdictNamesAreStable(t *testing.T) {
	for v, want := range map[Verdict]string{
		VerdictBlank:  "Blank",
		VerdictStatic: "Static",
		VerdictLive:   "Live",
		VerdictError:  "Error",
	} {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", v, got, want)
		}
	}
}

// TestScreenErrorReadsTheBottomLine pins the error scrape.
func TestScreenErrorReadsTheBottomLine(t *testing.T) {
	for _, tc := range []struct{ screen, want string }{
		{"some game\n\n\n0 OK, 0:1", ""},
		{"listing\n\nInteger out of range, 0:1", "Integer out of range, 0:1"},
		{"blah\n\nh File not found, 0:1", "h File not found, 0:1"},
		{"a\nb\nc", ""},
	} {
		if got := ScreenError(tc.screen); got != tc.want {
			t.Errorf("ScreenError(%q) = %q, want %q", tc.screen, got, tc.want)
		}
	}
}

// TestScreenTitleProducesAUsableScreening runs a real machine and pins that
// the harness measures what the classifier expects. The 48K ROM boots to the
// copyright screen: drawn, but not animating.
func TestScreenTitleProducesAUsableScreening(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Skipf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("never reached BASIC: %v", err)
	}
	s := h.ScreenTitle(96)
	if s.Pixels == 0 {
		t.Error("the boot screen measured zero ink")
	}
	if s.Colours == 0 {
		t.Error("the boot screen measured zero attributes")
	}
	if s.Error != "" {
		t.Errorf("the boot screen reported an error: %q", s.Error)
	}
	// A settled BASIC prompt is not animating (the cursor flashes via the
	// FLASH attribute bit, which does not change the stored bytes).
	if s.Moved {
		t.Error("the settled BASIC prompt reported as moving")
	}
}

// TestScreenFileRejectsUnknownFormats pins the guard: screening dispatches on
// extension, so an unhandled one must say so rather than silently report a
// blank screen, which would look exactly like a title that failed to run.
func TestScreenFileRejectsUnknownFormats(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Skipf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if _, err := h.ScreenFile("game.xyzzy", 100); err == nil {
		t.Error("an unknown extension was accepted")
	}
}

// TestScreenFileRunsASnapshot drives the whole path on a vendored corpus
// snapshot: load, run, measure, classify. These conformance programs draw a
// result screen, so a working emulator must classify them as content rather
// than blank.
func TestScreenFileRunsASnapshot(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Skipf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	s, err := h.ScreenFile("testdata/corpus/bin/ccffrm.sna", 200)
	if err != nil {
		t.Fatalf("ScreenFile: %v", err)
	}
	if v := Classify(s); v == VerdictBlank {
		t.Errorf("a conformance snapshot that draws a result screen classified as %v (pixels=%d colours=%d)",
			v, s.Pixels, s.Colours)
	}
}

// TestScreenSeesLayer2Content pins the flaw this caught. Screening originally
// measured the ULA display file at $4000, which is the wrong place for a Next
// title: Layer 2, the tilemap and the sprites all render from elsewhere. A
// Layer 2 demo therefore measured ink=0 and classified as Blank, which is the
// worst possible failure for a compatibility harness — it reports a working
// title as broken.
//
// The vendored tilemap demo measured ULA ink = 0 while rendering 8 distinct
// colours, so it is exactly the case that must not read as blank. Two of the
// three vendored Next demos failed this before the fix.
func TestScreenSeesLayer2Content(t *testing.T) {
	installtest.RedirectConfig(t)
	installOpenBottomROM(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Skipf("Next harness: %v", err)
	}
	defer h.CloseFiles()

	s, err := h.ScreenFile("testdata/corpus/bin/zxnext_tilemap.nex", 250)
	if err != nil {
		t.Fatalf("ScreenFile: %v", err)
	}
	if v := Classify(s); v == VerdictBlank {
		t.Errorf("a Layer 2 demo classified as Blank (pixels=%d colours=%d): screening is looking at the ULA bitmap, not the composited frame",
			s.Pixels, s.Colours)
	}
}

// TestScreenFileHandlesTZX pins .tzx support. Half the canonical Spectrum
// catalogue is distributed as TZX rather than TAP — it is the format that can
// carry custom loaders — so a screening harness that rejects it cannot verify
// most of the titles worth verifying.
func TestScreenFileHandlesTZX(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Skipf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	// Any TZX will do: the point is that the extension is routed, not rejected.
	_, err = h.ScreenFile("testdata/corpus/bin/nonexistent.tzx", 10)
	if err != nil && strings.Contains(err.Error(), "no screening loader") {
		t.Fatal(".tzx is rejected as an unknown format")
	}
}
