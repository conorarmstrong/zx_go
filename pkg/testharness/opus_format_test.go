package testharness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"github.com/conorarmstrong/zx_go/pkg/opus"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Formatting drives the WD1770's WRITE TRACK command: the ROM hands the
// controller a whole raw track a byte at a time through the NMI, gaps and sync
// and address marks included, and the controller picks the sectors out of it.
// These tests run the real thing — the Opus ROM's own FORMAT command, entered
// from BASIC — and then read the result back with the ROM's own CAT.

// typeFORMAT enters FORMAT (extended mode, SYMBOL SHIFT + 0) followed by tail,
// then answers the ROM's confirmation prompt.
//
// FORMAT will not touch a disk without asking: it reads the existing catalogue
// first and puts up 'Destroy "name"?', so the Y is not optional.
func typeFORMAT(h *Harness, tail string) {
	h.EnterExtendedMode()
	h.PressSymbolShift(true)
	h.RunFrames(2)
	h.TapKey("0")
	h.PressSymbolShift(false)
	h.RunFrames(3)
	for _, r := range tail {
		switch r {
		case '"':
			h.PressSymbolShift(true)
			h.RunFrames(2)
			h.TapKey("P")
			h.PressSymbolShift(false)
		case ';':
			h.PressSymbolShift(true)
			h.RunFrames(2)
			h.TapKey("O")
			h.PressSymbolShift(false)
		default:
			h.TapKey(fyne.KeyName(strings.ToUpper(string(r))))
		}
		h.RunFrames(3)
	}
	h.TapKey("Return")
	h.RunFrames(120)
	h.TapKey("Y") // Destroy "..."?
	// A full format is 40 tracks of 6250 raw bytes at one byte per NMI, so it
	// genuinely takes a few seconds of emulated time.
	h.RunFrames(3000)
}

// newOpus48K returns a 48K machine at the BASIC prompt with an Opus fitted and
// the image at path in drive 1.
func newOpus48K(t *testing.T, path string) *Harness {
	t.Helper()
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("48K harness: %v", err)
	}
	t.Cleanup(h.CloseFiles)
	if err := h.EnableOpus(); err != nil {
		t.Fatalf("EnableOpus: %v", err)
	}
	if err := h.InsertOpusDisk(0, path); err != nil {
		t.Fatalf("InsertOpusDisk: %v", err)
	}
	h.Reboot()
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("never reached BASIC: %v", err)
	}
	return h
}

// TestOpusFormatsABlankDiskAndCatalogsIt is the end-to-end proof of WRITE
// TRACK, and it needs no commercial disk: a blank image is formatted by the
// ROM and then read back by the ROM. If the controller mis-parsed the track
// stream the catalogue would not be readable at all.
func TestOpusFormatsABlankDiskAndCatalogsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.opd")
	if err := os.WriteFile(path, make([]byte, opus.ImageBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newOpus48K(t, path)

	typeFORMAT(h, `1;"TEST"`)
	if screen := h.ScreenText(); !strings.Contains(screen, "0 OK") {
		t.Fatalf("FORMAT did not report success\nscreen:\n%s", screen)
	}

	img := h.Opus().Dev.Disk(0)
	if !img.Modified() {
		t.Fatal("FORMAT did not write anything to the image")
	}
	// Every data track must now hold the format filler.
	for _, tr := range []int{1, 5, 20, 39} {
		sec, err := img.ReadSector(tr, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i, b := range sec {
			if b != 0xE5 {
				t.Fatalf("track %d sector 0 byte %d = %#02x, want the format filler 0xE5",
					tr, i, b)
			}
		}
	}

	// And the ROM must be able to read its own handiwork. BASIC types in
	// lower case, so the disk is named "test".
	typeCAT(h, "1")
	screen := h.ScreenText()
	if !strings.Contains(strings.ToUpper(screen), "TEST") {
		t.Errorf("CAT does not show the new disk name\nscreen:\n%s", screen)
	}
	if !strings.Contains(screen, "0 OK") {
		t.Errorf("CAT reported an error on the formatted disk\nscreen:\n%s", screen)
	}
	// A freshly formatted 40x18 disk has more free blocks than the game disk
	// this same catalogue reports 148 for, so this also pins that the format
	// actually reclaimed the whole surface.
	if !strings.Contains(screen, "178") {
		t.Errorf("CAT does not report a full free-block count of 178\nscreen:\n%s", screen)
	}
}

// TestOpusFormatOnACopyLeavesTheOriginalAlone formats a copy of a real disk
// and checks the source file byte for byte. Guest writes are held in the
// mounted image, so a format can never reach the file it was loaded from —
// this pins that, on the one operation where getting it wrong would destroy
// somebody's disk.
func TestOpusFormatOnACopyLeavesTheOriginalAlone(t *testing.T) {
	const src = "../../games/opus/BATTY.OPD"
	original, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("no Opus test disk available: %v", err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.opd")
	if err := os.WriteFile(copyPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	h := newOpus48K(t, copyPath)

	// The disk starts out as the game disk.
	typeCAT(h, "1")
	if screen := h.ScreenText(); !strings.Contains(screen, "SSSD") {
		t.Fatalf("precondition: the copy is not the original disk\nscreen:\n%s", screen)
	}

	typeFORMAT(h, `1;"WIPED"`)
	if screen := h.ScreenText(); !strings.Contains(screen, "0 OK") {
		t.Fatalf("FORMAT did not report success\nscreen:\n%s", screen)
	}

	// The mounted image really was wiped. Checked against the image rather
	// than the screen: the Spectrum scrolls instead of clearing, so the first
	// CAT's output is still sitting above the second one.
	img := h.Opus().Dev.Disk(0)
	for _, tr := range []int{1, 5, 20, 39} {
		sec, err := img.ReadSector(tr, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i, b := range sec {
			if b != 0xE5 {
				t.Fatalf("track %d sector 0 byte %d = %#02x: the game's data survived the format",
					tr, i, b)
			}
		}
	}
	if bytes.Contains(img.Bytes(), []byte("BAC")) {
		t.Error("a game filename survived the format")
	}

	typeCAT(h, "1")
	if screen := h.ScreenText(); !strings.Contains(strings.ToUpper(screen), "WIPED") {
		t.Errorf("CAT does not show the new disk name\nscreen:\n%s", screen)
	}

	// And neither file on disk was touched — not the source, not the copy,
	// because nothing is written back without an explicit save.
	for _, p := range []string{src, copyPath} {
		after, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, original) {
			t.Errorf("%s was modified by the format", p)
		}
	}
}

// TestOpusFormattedDiskSurvivesSaveAndReload pins that a formatted disk is a
// real artifact and not just something the running ROM happens to accept:
// saved back out, reloaded into a fresh machine, and catalogued again.
func TestOpusFormattedDiskSurvivesSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.opd")
	if err := os.WriteFile(path, make([]byte, opus.ImageBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newOpus48K(t, path)
	typeFORMAT(h, `1;"KEEPER"`)
	if screen := h.ScreenText(); !strings.Contains(screen, "0 OK") {
		t.Fatalf("FORMAT failed\nscreen:\n%s", screen)
	}

	saved := filepath.Join(dir, "formatted.opd")
	if err := os.WriteFile(saved, h.Opus().Dev.Disk(0).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(saved); err != nil || fi.Size() != opus.ImageBytes {
		t.Fatalf("saved image is not a well-formed .opd: %v", err)
	}

	// A brand new machine, mounting only the saved file.
	h2 := newOpus48K(t, saved)
	typeCAT(h2, "1")
	screen := h2.ScreenText()
	if !strings.Contains(strings.ToUpper(screen), "KEEPER") {
		t.Errorf("the reloaded disk does not catalogue\nscreen:\n%s", screen)
	}
	if !strings.Contains(screen, "0 OK") {
		t.Errorf("the reloaded disk reported an error\nscreen:\n%s", screen)
	}
}
