package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// The SAM Coupé, ZX80 and ZX81 are first-class machines with their own
// video and IO, and emu.ula is nil on all three. Save Snapshot and Load
// Snapshot dereferenced it for the border colour, so the File menu
// offered the classic formats on those machines and then took the whole
// process down with no error dialog.
func TestCreateSnapshotRefusesAMachineWithNoULA(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	snap, err := createSnapshotFromEmulator(emu)
	if err == nil {
		t.Fatalf("createSnapshotFromEmulator returned a snapshot (%v) instead of refusing", snap != nil)
	}
	if !strings.Contains(err.Error(), "SAM") {
		t.Errorf("error %q does not say which machine or what to use instead", err)
	}
}

func TestApplySnapshotRefusesAMachineWithNoULA(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	if err := applySnapshotToEmulator(emu, snapshot.New()); err == nil {
		t.Fatal("applySnapshotToEmulator accepted a snapshot on a machine with no ULA")
	}
}

// File → Open File and drag-and-drop route through loadFileByPath,
// which guarded only the ZX80/ZX81. On the SAM a .tap, .tzx, .rzx or
// snapshot went straight through to emu.ula and crashed.
func TestLoadFileByPathRefusesSpectrumFormatsOnTheSAM(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	for _, ext := range []string{".tap", ".tzx", ".rzx", ".z80", ".sna", ".szx", ".nex"} {
		err := emu.admitFileForMachine(ext)
		if err == nil {
			t.Errorf("%s was accepted on the SAM", ext)
			continue
		}
		if !strings.Contains(err.Error(), "SAM") {
			t.Errorf("%s: error %q does not say which machine or what to use instead", ext, err)
		}
	}
}

// The ZX80/ZX81 guard must keep admitting their own program formats,
// and a Spectrum must keep admitting everything.
func TestAdmitFileForMachineLeavesTheOtherMachinesAlone(t *testing.T) {
	zx, err := newZX8xEmulator(roms.ModelZX81)
	if err != nil {
		t.Skipf("newZX8xEmulator: %v", err)
	}
	for _, ext := range []string{".p", ".81", ".o", ".80"} {
		if err := zx.admitFileForMachine(ext); err != nil {
			t.Errorf("ZX81 refused its own %s: %v", ext, err)
		}
	}
	if err := zx.admitFileForMachine(".tap"); err == nil {
		t.Error("ZX81 accepted a .tap")
	}

	spectrum, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	for _, ext := range []string{".tap", ".tzx", ".z80", ".sna", ".szx", ".rzx"} {
		if err := spectrum.admitFileForMachine(ext); err != nil {
			t.Errorf("128K refused %s: %v", ext, err)
		}
	}
}
