package testharness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Covers the harness lifecycle helpers — Reboot, the SHIFT-modified
// key helpers, SaveScreenshot (savePNG), and Wait.

func TestHarness_Reboot(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.RunFrames(5)
	h.Reboot()
	// After reboot the CPU is back at the reset vector.
	if h.cpu.PC != 0x0000 {
		t.Errorf("after Reboot, PC = $%04X, want $0000", h.cpu.PC)
	}
}

func TestHarness_ShiftKeyHelpers(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// PressKeyShift asserts CAPS SHIFT (row 0, bit 0) alongside the
	// base key; ReleaseKeyShift clears it. Row 0 is read via the
	// keyboard port at $FEFE, active-low (0 = pressed).
	h.PressKeyShift(fyne.Key0)
	if h.kbd.Scan(0xFEFE)&0x01 != 0 {
		t.Error("PressKeyShift did not assert CAPS SHIFT (row0 bit0)")
	}
	h.ReleaseKeyShift(fyne.Key0)
	if h.kbd.Scan(0xFEFE)&0x01 == 0 {
		t.Error("ReleaseKeyShift did not clear CAPS SHIFT")
	}
	// TapKeyShift should run frames without panicking and end with the
	// key released.
	h.TapKeyShift(fyne.KeyA)
}

func TestHarness_SaveScreenshot(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.RunFrames(2)
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := h.SaveScreenshot(path); err != nil {
		t.Fatalf("SaveScreenshot: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() == 0 {
		t.Error("screenshot file is empty")
	}
	// Error path: a directory that doesn't exist.
	if err := h.SaveScreenshot("/no/such/dir/shot.png"); err == nil {
		t.Error("SaveScreenshot to bad path should error")
	}
}

func TestHarness_Wait(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := h.cpu.InstructionCount()
	h.Wait(40 * time.Millisecond) // ~2 frames
	if h.cpu.InstructionCount() == before {
		t.Error("Wait did not advance the CPU")
	}
	// Sub-frame duration still runs at least one frame.
	h.Wait(1 * time.Millisecond)
}
