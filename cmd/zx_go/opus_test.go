package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/opus"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestOpusIsFittedOnMount pins the emulator-side wiring: mounting a .opd must
// build the interface, install the ROM overlay, and reset the machine so the
// Opus reset vector at $0000 actually runs.
func TestOpusIsFittedOnMount(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Skipf("48K core unavailable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "blank.opd")
	if err := os.WriteFile(path, make([]byte, opus.ImageBytes), 0o600); err != nil {
		t.Fatal(err)
	}

	rebooted, err := emu.mountOPD(0, path)
	if err != nil {
		t.Fatalf("mountOPD: %v", err)
	}
	if !rebooted {
		t.Error("the machine was not reset when the interface was first fitted")
	}
	if emu.opus == nil {
		t.Fatal("no Opus interface was created")
	}
	if !emu.opus.Active() {
		t.Error("the Opus ROM is not paged in after the reset")
	}
	// The overlay must actually be reaching the memory system.
	rom, err := roms.ReadEmbeddedROM("opus.rom")
	if err != nil {
		t.Fatal(err)
	}
	if got := emu.mem.Read(0x0000); got != rom[0] {
		t.Errorf("$0000 reads %#02x, want the Opus reset vector %#02x", got, rom[0])
	}

	// A second mount must not reset again.
	rebooted, err = emu.mountOPD(1, path)
	if err != nil {
		t.Fatalf("second mountOPD: %v", err)
	}
	if rebooted {
		t.Error("mounting a second disk reset the machine")
	}
}

// TestOpusRejectsNon48KModels pins the guard. The interface traps fixed
// addresses in the 48K ROM, so fitting it to anything else would substitute
// its code at addresses that mean something different.
func TestOpusRejectsNon48KModels(t *testing.T) {
	for _, m := range []roms.SpectrumModel{roms.Model128K, roms.ModelPlus3, roms.ModelPentagon} {
		emu, err := newEmulator(m)
		if err != nil {
			continue
		}
		if emu.opusSupported() {
			t.Errorf("%s reports Opus support", roms.GetModelName(m))
		}
		if _, err := emu.ensureOpus(); err == nil {
			t.Errorf("%s accepted an Opus Discovery", roms.GetModelName(m))
		}
	}
}

// TestOpusRejectsAWrongSizedImage guards the loader: .opd has no header, so
// its size is the only validation there is.
func TestOpusRejectsAWrongSizedImage(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Skipf("48K core unavailable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "short.opd")
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := emu.mountOPD(0, path); err == nil {
		t.Error("a 1 KB file was accepted as an Opus disk image")
	}
}
