package main

import (
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNexImportAndLoadIfPresent exercises the File -> Open .nex pipeline end to
// end: insert a .nex into the live SD-card image (as confirmImportNex does),
// then drive the NextZXOS .nexload macro from a run-loop-style frame loop and
// confirm the game launches. The in-memory image is modified but never
// persisted, so the on-disk SD image is left untouched. Skipped when the Next
// ROMs / SD image (gitignored) are absent.
func TestNexImportAndLoadIfPresent(t *testing.T) {
	const host = "../../roms/next/sd/games/Next/Warhawk/Warhawk.nex"
	data, err := os.ReadFile(host)
	if err != nil {
		t.Skipf("%s not present (gitignored)", host)
	}

	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	t.Cleanup(func() { cliFlagsActive = prev })

	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("Next ROMs not installed: %v", err)
	}
	if emu.sdImageSrc == nil {
		t.Skip("Next SD card is not an image (no sdImageSrc); import path not exercised")
	}

	// Insert into the live in-memory image (no WriteBackTo: the on-disk
	// image is not modified).
	sdPath, err := sdcard.AddFileToFAT32(emu.sdImageSrc.Bytes(), "imported", "warhawk.nex", data)
	if err != nil {
		t.Fatalf("AddFileToFAT32: %v", err)
	}
	if sdPath != "/IMPORTED/WARHAWK.NEX" {
		t.Errorf("sdPath = %q, want /IMPORTED/WARHAWK.NEX", sdPath)
	}

	// Drive the loader macro exactly as the run loop does.
	emu.startNexloadMacro(sdPath)
	for f := 0; f < 6000 && emu.nexloadMacro != nil; f++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
		if emu.nexloadMacro.tick(emu) {
			emu.nexloadMacro = nil
		}
	}

	img := emu.renderFrame()
	if uniformImage(img) {
		t.Errorf("screen blank after import+load — game did not render")
	}
	if emu.cpu.PC == nextMenuLoopPC {
		t.Errorf("returned to the NextZXOS menu (PC=%#04x) — game did not launch", emu.cpu.PC)
	}
	t.Logf("imported + launched %s: PC=%#04x", sdPath, emu.cpu.PC)
}
