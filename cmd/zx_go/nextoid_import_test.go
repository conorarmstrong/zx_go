package main

import (
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNextoidImportPathReboot reproduces the GUI File->Open flow: import the
// .nex into the SD image's /imported/ folder (in-memory; sdImagePath is empty
// so nothing is persisted) then drive the nexload macro, and confirms the
// game reaches gameplay instead of rebooting back to the NextZXOS welcome
// loop. Gated by NEXTOID_E2E.
func TestNextoidImportPathReboot(t *testing.T) {
	if os.Getenv("NEXTOID_E2E") == "" {
		t.Skip("set NEXTOID_E2E=1")
	}
	hostNex := "/Users/conorarmstrong/development/zx_go/roms/next/sd/games/Next/Nextoid/nextoid.nex"
	data, err := os.ReadFile(hostNex)
	if err != nil {
		t.Skipf("no host nextoid.nex: %v", err)
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
		t.Skipf("no ROMs: %v", err)
	}
	if emu.sdImageSrc == nil {
		t.Skip("no SD image source (folder mode) — File->Open import unavailable")
	}
	save := func(name string, img image.Image) {
		fp, _ := os.Create(name)
		_ = png.Encode(fp, img)
		_ = fp.Close()
	}
	step := func() {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
		if emu.nexloadMacro != nil && emu.nexloadMacro.tick(emu) {
			emu.nexloadMacro = nil
		}
	}

	// Exactly what File->Open does (headless branch of confirmImportNex calls
	// importAndRunNex directly). sdImagePath is empty -> no disk writeback.
	emu.importAndRunNex("nextoid.nex", data)
	t.Logf("after import: sdImagePath=%q macro=%v", emu.sdImagePath, emu.nexloadMacro != nil)

	for f := 0; f < 4500 && emu.nexloadMacro != nil; f++ {
		step()
	}
	for f := 0; f < 60; f++ {
		step()
	}
	save("/tmp/nextoid_import_menu.png", emu.renderFrame())
	t.Logf("after macro: PC=$%04X", emu.cpu.PC)

	// Try to start the game and watch for a reboot to the welcome loop.
	press := func(row int, mask byte) {
		for f := 0; f < 25; f++ {
			emu.kbd.PressMatrixKey(row, mask, true)
			step()
		}
		emu.kbd.PressMatrixKey(row, mask, false)
		for f := 0; f < 25; f++ {
			step()
		}
	}
	press(1, 0x02) // 'S'
	press(7, 0x01) // SPACE
	welcomeHits := 0
	for f := 0; f < 1500; f++ {
		step()
		if emu.cpu.PC == nextMenuLoopPC {
			welcomeHits++
		}
		if f%500 == 0 {
			save("/tmp/nextoid_import_play.png", emu.renderFrame())
		}
	}
	save("/tmp/nextoid_import_play.png", emu.renderFrame())
	visible := 0
	for i := 0; i < 128; i++ {
		if emu.nextSprites.Sprite(i).Visible {
			visible++
		}
	}
	t.Logf("FINAL PC=$%04X welcomeHits=%d visibleSprites=%d", emu.cpu.PC, welcomeHits, visible)
	// The File->Open path must reach gameplay, NOT reboot back to the
	// NextZXOS welcome loop.
	if welcomeHits > 0 {
		t.Fatalf("game rebooted to the NextZXOS welcome loop (%d hits) instead of running", welcomeHits)
	}
	if visible == 0 {
		t.Fatal("no gameplay sprites after the File->Open import path")
	}
}
