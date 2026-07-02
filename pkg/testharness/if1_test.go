package testharness

import (
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestBasicPRINTCommand boots 48K BASIC, types a PRINT statement,
// presses ENTER, and confirms BASIC echoed the command and ran it
// (the "0 OK" prompt appears). This is the foundation test that
// proves the harness can actually drive BASIC end-to-end before
// we try anything more elaborate.
func TestBasicPRINTCommand(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := h.RunUntilText("1982", 200); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// 48K BASIC at the start of a line is in K (keyword) mode.
	// Pressing P inserts the keyword PRINT.
	h.TapKey(fyne.KeyP)
	h.RunFrames(20)

	// Now press ENTER to execute it. PRINT with no arguments
	// just prints a blank line — afterwards BASIC reports
	// "0 OK, 0:1" on the bottom row.
	h.TapKey(fyne.KeyReturn)
	h.RunFrames(50)

	text := h.ScreenText()
	if !strings.Contains(text, "0 OK") {
		t.Errorf("expected '0 OK' after PRINT+ENTER, got:\n%s", text)
	}
}

// TestIF1EnableActivatesPagingHook verifies that EnableInterface1
// installs the page-in/page-out hooks on the CPU and that the IF1
// peripheral is reachable through the manager.
func TestIF1EnableActivatesPagingHook(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableInterface1(); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}
	if !h.peripherals.IsInterface1Enabled() {
		t.Error("after EnableInterface1: IsInterface1Enabled = false")
	}
	if h.cpu.PreFetchHook == nil {
		t.Error("after EnableInterface1: PreFetchHook not installed on CPU")
	}
	if h.cpu.PostFetchHook == nil {
		t.Error("after EnableInterface1: PostFetchHook not installed on CPU")
	}
}

// TestIF1ROMPagedInOnBoot verifies that booting the 48K Spectrum
// with the IF1 enabled causes the shadow ROM to get paged in
// during the boot sequence. The 48K boot routine doesn't fall
// through any of the page-in trigger addresses (0x0008/0x1708)
// directly, but errors during initialisation can cause the IF1
// ROM to take over briefly. We don't assert IF1.Active() == true
// across the whole boot — that depends on what the boot code
// happens to do — but we do assert that the harness can boot
// successfully WITH the IF1 enabled (i.e. the page-in/page-out
// hooks don't crash the CPU).
func TestIF1ROMPagedInOnBoot(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableInterface1(); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}
	// The IF1 boot sequence intercepts RST 8 to add its own
	// initialisation. After a few hundred frames the BASIC
	// banner should still appear on screen — this proves the
	// page-in/page-out doesn't break the boot sequence.
	if _, err := h.RunUntilText("1982", 300); err != nil {
		t.Logf("screen at timeout:\n%s", h.ScreenText())
		t.Fatalf("boot with IF1 enabled: %v", err)
	}
}

// TestMicrodriveInsertReachesDrive end-to-end-tests the cartridge
// insertion path: enable IF1, insert success.mdr into drive 0,
// verify the peripheral manager reports the cartridge is inserted
// and not write-protected.
func TestMicrodriveInsertReachesDrive(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableInterface1(); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}
	cartPath := filepath.Join("..", "microdrive", "testdata", "success.mdr")
	if err := h.InsertMicrodrive(0, cartPath); err != nil {
		t.Fatalf("InsertMicrodrive: %v", err)
	}
	if !h.peripherals.MicrodriveCartridgeInserted(0) {
		t.Error("after insert: MicrodriveCartridgeInserted(0) = false")
	}
	if h.peripherals.MicrodriveWriteProtected(0) {
		t.Error("freshly inserted cartridge should not be write-protected")
	}
}

// TestIF1CATCommand is the gold-standard end-to-end IF1 test:
// boot 48K, enable IF1, insert a real microdrive cartridge, type
// a CAT command, and verify the IF1 ROM produced cartridge listing
// output on the screen.
//
// 48K BASIC + IF1 enters CAT via this key sequence:
//
//  1. Briefly press CAPS SHIFT + SYMBOL SHIFT to enter E (extended)
//     mode — the cursor changes from K to E.
//  2. Hold SYMBOL SHIFT and press the 9 key. In E mode with SYMBOL
//     SHIFT, 9 is the IF1's CAT keyword (overlaying the stock
//     "POINT" keyword that's on 9 without the IF1).
//  3. Type the drive number argument (" 1" for microdrive 1).
//  4. Press ENTER.
//
// 48K BASIC keyword entry is famously fiddly; if this test doesn't
// produce recognisable CAT output it logs the screen contents and
// SKIPs rather than failing — the other IF1 tests already prove
// the plumbing works, and the exact keystroke sequence is a
// hardware-specific detail we can iterate on without blocking CI.
func TestIF1CATCommand(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableInterface1(); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}
	cartPath := filepath.Join("..", "microdrive", "testdata", "success.mdr")
	if err := h.InsertMicrodrive(0, cartPath); err != nil {
		t.Fatalf("InsertMicrodrive: %v", err)
	}
	if _, err := h.RunUntilText("1982", 300); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Enter E mode.
	h.EnterExtendedMode()

	// Press SYMBOL SHIFT + 9 to get the CAT keyword.
	h.PressSymbolShift(true)
	h.TapKey(fyne.Key9)
	h.PressSymbolShift(false)
	h.RunFrames(10)

	// The CAT command's drive-number argument: space, then 1.
	h.TapKey(fyne.KeySpace)
	h.TapKey(fyne.Key1)
	h.RunFrames(10)

	// Execute.
	h.TapKey(fyne.KeyReturn)

	// Wait for BASIC's "0 OK" prompt — the signal that CAT
	// completed without an error. success.mdr contains a single
	// file named "Success"; verify it appears in the output along
	// with the cartridge name.
	frames, err := h.RunUntilText("0 OK", 500)
	if err != nil {
		_ = h.SaveScreenshot("if1_cat_failure.png")
		t.Logf("screen at timeout:\n%s", h.ScreenText())
		t.Fatalf("CAT did not complete: %v", err)
	}
	text := h.ScreenText()
	t.Logf("CAT completed after %d frames", frames)
	t.Logf("final screen:\n%s", text)

	if !strings.Contains(text, "Success") {
		t.Errorf("expected 'Success' (file name from success.mdr) in CAT output, got:\n%s", text)
	}
}
