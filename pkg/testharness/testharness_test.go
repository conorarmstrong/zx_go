package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNewBoots48K constructs a 48K Spectrum harness and runs a few
// hundred frames — long enough for the BASIC ROM to clear the
// screen, draw the copyright banner, and reach the cursor prompt.
// We then peek at the screen memory and confirm that *something*
// has been written to it (the screen wasn't empty after boot).
func TestNewBoots48K(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// At t=0 the screen RAM is whatever the boot sequence left in
	// it (initially zero). We boot through ~150 frames (3 seconds
	// of simulated time, well past BASIC's clear-screen routine).
	h.RunFrames(150)

	// Sanity check: the boot sequence should have written
	// non-zero data into the attribute area. The bitmap area
	// might be all zeros if BASIC just shows a blank screen, but
	// the attributes are set to PAPER 7 / INK 0 by the clear
	// routine — that's 0x38 in every cell.
	allZero := true
	for addr := uint16(0x5800); addr < 0x5B00; addr++ {
		if h.Memory(addr) != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("after 150 frames: attribute area is all zeros (BASIC didn't boot?)")
	}
}

// TestRunFramesAdvancesPC verifies that RunFrames actually moves
// the CPU forward — checks that the PC changes between two
// consecutive RunFrames calls.
func TestRunFramesAdvancesPC(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pc1 := h.cpu.PC
	h.RunFrames(1)
	pc2 := h.cpu.PC
	h.RunFrames(1)
	pc3 := h.cpu.PC

	if pc1 == pc2 && pc2 == pc3 {
		t.Errorf("PC didn't advance: %04X → %04X → %04X", pc1, pc2, pc3)
	}
}

// TestMemoryReadWrite verifies the memory accessor round-trip via
// the harness API.
func TestMemoryReadWrite(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.WriteMemory(0x8000, 0xAB)
	if got := h.Memory(0x8000); got != 0xAB {
		t.Errorf("Memory(0x8000) = %02X, want 0xAB", got)
	}
}

// TestRunUntilTimeout verifies the predicate-based wait helper
// returns an error when the predicate never fires.
func TestRunUntilTimeout(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = h.RunUntil(func(*Harness) bool { return false }, 5)
	if err == nil {
		t.Error("RunUntil with always-false predicate: want timeout error, got nil")
	}
}
