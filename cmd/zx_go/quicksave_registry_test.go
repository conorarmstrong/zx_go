package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Quick-save on the machines .szx cannot express.
//
// .sna, .z80 and .szx all describe a 48K/128K memory map and a Z80. None of
// them has anywhere to put the Next's 2 MB and its NextRegs, the SAM's
// LMPR/HMPR/VMPR paging and SAA1099, or the ZX81's CPU-generated display, so
// quick-save refused all three outright. Each of them does have a machinestate
// registry — the same one the rewind ring uses — so the state gets written from
// that instead.

// redirectQuickSave points the slot at a temp file for the duration of a test.
// The name carries an extension because the SZX writer picks its format from
// one; the registry writer does not care.
func redirectQuickSave(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	prev := quickSaveSlotOverride
	quickSaveSlotOverride = path
	t.Cleanup(func() { quickSaveSlotOverride = prev })
	return path
}

// The SAM saves and loads, which it could not do at all before it had a
// registry.
func TestQuickSaveRoundTripsOnTheSAM(t *testing.T) {
	redirectQuickSave(t, "quicksave.zxgostate")
	emu := newSAMForState(t)
	for i := 0; i < 60; i++ {
		emu.sam.RunFrame()
	}

	before := emu.stateRegistry().Capture()
	if err := emu.quickSaveState(); err != nil {
		t.Fatalf("quickSaveState: %v", err)
	}

	for i := 0; i < 30; i++ {
		emu.sam.RunFrame()
	}
	if err := emu.quickLoadState(); err != nil {
		t.Fatalf("quickLoadState: %v", err)
	}

	after := emu.stateRegistry().Capture()
	if string(after.Bytes()) != string(before.Bytes()) {
		t.Error("the SAM did not come back as the machine that was saved")
	}
}

// The saved file is our container, tagged with the machine, so a wrong-machine
// load can say what actually happened rather than failing as a device-set
// mismatch.
func TestTheSavedFileIsTaggedWithItsMachine(t *testing.T) {
	path := redirectQuickSave(t, "quicksave.zxgostate")
	emu := newSAMForState(t)
	if err := emu.quickSaveState(); err != nil {
		t.Fatalf("quickSaveState: %v", err)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the slot: %v", err)
	}
	machine, _, err := machinestate.Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if machine != roms.GetModelName(roms.ModelSAM) {
		t.Errorf("machine tag = %q, want %q", machine, roms.GetModelName(roms.ModelSAM))
	}
}

// Loading a state saved on a different machine is refused, and the message says
// which machine it came from. Without the tag the error would be a list of
// device names, which is accurate and unhelpful.
func TestLoadingAnotherMachinesStateIsRefusedByName(t *testing.T) {
	path := redirectQuickSave(t, "quicksave.zxgostate")
	if err := os.WriteFile(path, machinestate.Encode("Spectrum Next", machinestate.State{}), 0o600); err != nil {
		t.Fatal(err)
	}

	emu := newSAMForState(t)
	err := emu.quickLoadState()
	if err == nil {
		t.Fatal("a Next save state was loaded into a SAM")
	}
	if !strings.Contains(err.Error(), "Spectrum Next") {
		t.Errorf("error = %q, want it to name the machine the state came from", err)
	}
}

// The classic Spectrums keep using .szx, which is portable and which other
// emulators can read. Switching them to our own container would be a
// regression dressed as consistency.
func TestTheClassicModelsStillSaveSZX(t *testing.T) {
	path := redirectQuickSave(t, "quicksave.szx")
	emu := quietEmulator(t, roms.Model128K)
	if err := emu.quickSaveState(); err != nil {
		t.Fatalf("quickSaveState: %v", err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < 4 || string(blob[:4]) != "ZXST" {
		t.Errorf("128K quick-save is not an SZX file (starts %q)", blob[:min(4, len(blob))])
	}
}

// Quick-load with nothing saved says so rather than reporting a parse failure.
func TestQuickLoadWithNoSlotSaysSo(t *testing.T) {
	redirectQuickSave(t, "quicksave.zxgostate")
	emu := newSAMForState(t)
	if err := emu.quickLoadState(); err == nil {
		t.Fatal("quickLoadState with no slot returned nil")
	}
}
