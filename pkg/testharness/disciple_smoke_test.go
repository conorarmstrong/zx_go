package testharness

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestDiscipleColdBootAndKeypress enables the DISCiPLE from power-on
// (romPaged=true), boots the 48K Spectrum through the GDOS init,
// and verifies BASIC is responsive by tapping a key and checking
// for the expected keyword on screen. This is the definitive
// end-to-end test that the DISCiPLE cold boot works.
func TestDiscipleColdBootAndKeypress(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDisciple(); err != nil {
		t.Fatalf("EnableDisciple: %v", err)
	}
	diskPath := buildTestMGT(t)
	if err := h.LoadDiscipleDisk(0, diskPath); err != nil {
		t.Fatalf("LoadDiscipleDisk: %v", err)
	}

	// Boot: GDOS runs at 0x0000, initializes, pages out, Spectrum
	// ROM continues at 0x11CB. RAM fill takes ~23 frames, then
	// system variables init, copyright, and BASIC prompt.
	h.RunFrames(500)

	// Type P — in K mode this produces "PRINT" keyword.
	h.TapKey(fyne.KeyP)
	h.RunFrames(20)

	text := h.ScreenText()
	if !strings.Contains(text, "PRINT") {
		t.Errorf("BASIC not responding after DISCiPLE cold boot. Screen:\n%s", text)
	}
}
