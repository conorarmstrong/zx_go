package main

import "testing"

// TestNexImportSDCardAvailableInFolderMode pins the fix for the File->Open
// .nex flow being wrongly blocked with "needs a Spectrum Next SD card (none
// is configured)".
//
// The .nex load path (main.go) and the importer (confirmImportNex) both gate
// on emu.sdImageSrc != nil so the .nex can be written into the live in-memory
// FAT32 image the guest reads. That field used to be set ONLY in raw-image
// mode AND only under --sd-writeback. In folder mode (the default, roms/next/sd)
// it stayed nil, so every GUI .nex load was blocked even with an SD configured.
//
// Behaviour under test: when an SD card is configured (folder mode here, the
// default the test harness uses), the emulator exposes the importable SD
// image so .nex loading is allowed.
func TestNexImportSDCardAvailableInFolderMode(t *testing.T) {
	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("Next ROMs / SD not installed: %v", err)
	}
	if emu.sdImageSrc == nil {
		t.Fatal("emu.sdImageSrc is nil with an SD configured (folder mode) — " +
			"File->Open .nex is wrongly blocked with 'needs a Spectrum Next SD card'")
	}
}
