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

// Quick-save is gated to machines with an SZX representation.
func TestQuickSaveSupportedGating(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		want  bool
	}{
		{roms.Model48K, true},
		{roms.ModelPentagon, true},
		{roms.ModelZX81, false}, // no ULA
		{roms.ModelNext, false}, // SZX doesn't capture full Next state
	} {
		emu, err := newEmulator(tc.model)
		if err != nil {
			t.Fatalf("newEmulator(%s): %v", roms.GetModelName(tc.model), err)
		}
		if got := emu.quickSaveSupported(); got != tc.want {
			t.Errorf("%s: quickSaveSupported = %v, want %v", roms.GetModelName(tc.model), got, tc.want)
		}
	}
}
