package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Quick-save then quick-load must round-trip a register value through the slot.
func TestQuickSaveLoadRoundTrip(t *testing.T) {
	quickSaveSlotOverride = filepath.Join(t.TempDir(), "qs.szx")
	t.Cleanup(func() { quickSaveSlotOverride = "" })

	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	emu.cpu.A = 0x5A
	if err := emu.quickSaveState(); err != nil {
		t.Fatalf("quickSaveState: %v", err)
	}
	if _, err := os.Stat(quickSaveSlotOverride); err != nil {
		t.Fatalf("slot file not written: %v", err)
	}

	emu.cpu.A = 0x00 // clobber, then restore
	if err := emu.quickLoadState(); err != nil {
		t.Fatalf("quickLoadState: %v", err)
	}
	if emu.cpu.A != 0x5A {
		t.Errorf("A after quick-load = %02X, want 5A", emu.cpu.A)
	}
}

// Which format a machine's quick-save takes.
//
// The classic Spectrums go through SZX because it is portable and other
// emulators read it. The ZX81 and the Next go through the machinestate registry
// instead: SZX describes a 48K/128K memory map and a Z80, and there is nowhere
// in it for the ZX81's CPU-generated display or the Next's 2 MB and NextRegs.
// Both used to be refused outright, which was the right call while SZX was the
// only option.
func TestQuickSaveFormatPerModel(t *testing.T) {
	for _, tc := range []struct {
		model   roms.SpectrumModel
		wantSZX bool
	}{
		{roms.Model48K, true},
		{roms.ModelPentagon, true},
		{roms.ModelZX81, false},
		{roms.ModelNext, false},
	} {
		emu, err := newEmulator(tc.model)
		if err != nil {
			t.Fatalf("newEmulator(%s): %v", roms.GetModelName(tc.model), err)
		}
		name := roms.GetModelName(tc.model)
		if got := emu.snapshotQuickSave(); got != tc.wantSZX {
			t.Errorf("%s: snapshotQuickSave = %v, want %v", name, got, tc.wantSZX)
		}
		// Every machine the emulator builds has something to save.
		if !emu.quickSaveSupported() {
			t.Errorf("%s: quick-save is unavailable", name)
		}
	}
}

// The two formats must not share a slot file: handing an SZX to the registry
// loader, or the reverse, would fail as a parse error rather than as the model
// mismatch it is.
func TestTheTwoFormatsUseDifferentSlots(t *testing.T) {
	quickSaveSlotOverride = ""
	classic, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	zx81, err := newEmulator(roms.ModelZX81)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	if a, b := classic.quickSavePath(), zx81.quickSavePath(); a == b {
		t.Errorf("both formats save to %q", a)
	}
}
