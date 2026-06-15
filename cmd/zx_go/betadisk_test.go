package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/betadisk"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// blankTRD writes a zero-filled 640 KB (80-track double-sided) TR-DOS image to a
// temp file and returns its path.
func blankTRD(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blank.trd")
	if err := os.WriteFile(path, make([]byte, 80*2*16*256), 0o644); err != nil {
		t.Fatalf("write blank TRD: %v", err)
	}
	return path
}

// Mounting a .TRD on the Pentagon enables the Beta interface, installs the
// TR-DOS ROM (so $3Dxx auto-paging works), and wires the FDC into the ULA.
func TestMountTRDOnPentagon(t *testing.T) {
	emu, err := newEmulator(roms.ModelPentagon)
	if err != nil {
		t.Fatalf("newEmulator(Pentagon): %v", err)
	}
	if err := emu.mountTRD(0, blankTRD(t)); err != nil {
		t.Fatalf("mountTRD: %v", err)
	}
	if emu.betaDisk == nil {
		t.Fatal("betaDisk still nil after mount")
	}
	// The TR-DOS ROM auto-pages in on a $3Dxx fetch once 48 BASIC is selected.
	emu.mem.PageMemory(0x10) // select 48 BASIC ROM
	emu.mem.BetaPreFetch(0x3D13)
	if !emu.mem.IsBetaActive() {
		t.Error("TR-DOS ROM did not page in on a $3Dxx fetch")
	}
	// A Restore command through the FDC completes and raises INTRQ.
	emu.betaDisk.FDC.WriteCommand(0x00)
	if !emu.betaDisk.FDC.Intrq() {
		t.Error("FDC Restore did not raise INTRQ")
	}
}

// TR-DOS is rejected on machines that don't support the Beta interface.
func TestMountTRDRejectedOnNext(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Fatalf("newEmulator(Next): %v", err)
	}
	if err := emu.mountTRD(0, blankTRD(t)); err == nil {
		t.Error("mountTRD on the Next should be rejected")
	}
}

// A malformed (wrong-size) image is rejected with a clear error.
func TestMountTRDBadImage(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator(48K): %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.trd")
	if err := os.WriteFile(bad, make([]byte, 123), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := emu.mountTRD(0, bad); err == nil {
		t.Error("mountTRD of a non-TRD-sized file should error")
	}
}

// Sanity: a blank image is the size LoadImage expects (guards the test helper).
func TestBlankTRDSize(t *testing.T) {
	data, err := os.ReadFile(blankTRD(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := betadisk.LoadImage(data); err != nil {
		t.Errorf("blank TRD not a valid image: %v", err)
	}
}
