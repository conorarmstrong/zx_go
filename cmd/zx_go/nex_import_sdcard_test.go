package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
)

// TestNexImportSDCardAvailableInFolderMode guards the File->Open .nex flow
// against being wrongly blocked with "needs a Spectrum Next SD card (none is
// configured)".
//
// The .nex load path (main.go) and the importer (confirmImportNex) both gate
// on emu.sdImageSrc != nil so the .nex can be written into the live in-memory
// FAT32 image the guest reads. That field must be populated whenever an SD
// card is configured — folder mode (the default, roms/next/sd) included, not
// just raw-image mode under --sd-writeback.
//
// Behaviour under test: when an SD card is configured (folder mode here, the
// default the test harness uses), the emulator exposes the importable SD
// image so .nex loading is allowed.
func TestNexImportSDCardAvailableInFolderMode(t *testing.T) {
	// Premise: an SD card is configured. With neither a host .img
	// (ZX_GO_NEXT_SD_IMG) nor a folder-mode SD tree (roms/next/sd) present —
	// the case in CI, where the SD content is gitignored — there is nothing to
	// import into, so the "sdImageSrc must be non-nil" assertion does not apply.
	// Skip rather than fail; the regression is still guarded locally where the
	// SD tree exists.
	if install.SDCardImage() == "" && install.SDCardRoot() == "" {
		t.Skip("no Spectrum Next SD card configured (folder/img absent) — nothing to import into")
	}
	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("Next ROMs / SD not installed: %v", err)
	}
	if emu.sdImageSrc == nil {
		t.Fatal("emu.sdImageSrc is nil with an SD configured (folder mode) — " +
			"File->Open .nex is wrongly blocked with 'needs a Spectrum Next SD card'")
	}
}
