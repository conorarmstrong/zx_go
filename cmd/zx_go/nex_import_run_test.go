package main

import (
	"image/png"
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNexImportAndRunFolderMode exercises the exact File->Open .nex user flow
// end-to-end in folder mode (the default): import a host .nex into the live
// in-memory SD image (under /imported/) and load it through NextZXOS's own
// .nexload. Guards against folder mode gating .nex loads on "no SD card
// configured" — sdImageSrc must be populated whenever an SD card is
// configured, not just in raw-image mode. Gated by SONIC_DIAG since it needs
// the local Next ROMs + a .nex on disk.
func TestNexImportAndRunFolderMode(t *testing.T) {
	if os.Getenv("SONIC_DIAG") == "" {
		t.Skip("set SONIC_DIAG=1 to run the import-and-run end-to-end check")
	}
	// A real host-side .nex the user would File->Open. The repo SD tree
	// carries sonic.nex; treat it as an arbitrary host file to import.
	const hostNex = "/Users/conorarmstrong/development/zx_go/roms/next/sd/games/Next/Sonic/sonic.nex"
	data, err := os.ReadFile(hostNex)
	if err != nil {
		t.Skipf("no host .nex to import: %v", err)
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
		t.Skipf("Next ROMs / SD not installed: %v", err)
	}
	if emu.sdImageSrc == nil {
		t.Fatal("sdImageSrc nil — import path unavailable (the bug)")
	}

	// The genuine GUI import + load path (window==nil → runs inline).
	emu.importAndRunNex("sonic.nex", data)

	// Drive frames until the imported game's entry executes. Sonic's entry
	// at $9000 begins ED 91 (LDDX) — distinct from the cold-boot spin there
	// (DD 96 F4), so this proves the imported file was found, loaded by
	// .nexload, and jumped to.
	entered := false
	emu.cpu.AddPreFetchHook("import-entry", func(pc uint16) {
		if pc == 0x9000 && emu.mem.Read(0x9000) == 0xED && emu.mem.Read(0x9001) == 0x91 {
			entered = true
		}
	})

	const totalFrames = 2500
	for f := 0; f < totalFrames && !entered; f++ {
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
	if !entered {
		t.Fatal("imported .nex never reached its entry — the import/load path is broken")
	}
	t.Log("imported .nex loaded + ran via NextZXOS .nexload (folder mode)")

	// Let it run on and render what the user would see.
	for f := 0; f < 1500; f++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
	}
	if fp, err := os.Create("/tmp/sonic_imported.png"); err == nil {
		_ = png.Encode(fp, emu.renderFrame())
		_ = fp.Close()
		t.Log("wrote /tmp/sonic_imported.png")
	}
}
