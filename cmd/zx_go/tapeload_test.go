package main

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestLoadTapeFileMountsAndPlays is the lock-in for the --tape headless flag
// (issue #5): given a .tap path, loadTapeFile mounts it into the ULA tape
// player and starts it playing — the same effect the GUI "Open .tap" path has.
func TestLoadTapeFileMountsAndPlays(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Skipf("emulator construction failed: %v", err)
	}
	path := writeTempTAP(t, 0x00, []byte("hello"))
	if err := loadTapeFile(emu, path); err != nil {
		t.Fatalf("loadTapeFile: %v", err)
	}
	tp := emu.ula.GetTapePlayer()
	if tp == nil {
		t.Fatal("no tape player mounted after loadTapeFile")
	}
	if tp.BlockCount() == 0 {
		t.Fatal("mounted tape has no blocks")
	}
	if !tp.IsPlaying() {
		t.Fatal("tape was mounted but not started playing")
	}
}

// TestLoadTapeFileRejectsUnknownExtension keeps the supported set explicit:
// only .tap/.tzx are tape formats; anything else is a clear error, not a
// silently mis-routed load.
func TestLoadTapeFileRejectsUnknownExtension(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Skipf("emulator construction failed: %v", err)
	}
	if err := loadTapeFile(emu, filepath.Join(t.TempDir(), "game.sna")); err == nil {
		t.Fatal("expected an error for an unsupported tape extension")
	}
}

// TestLoadTapeFileMissingFile surfaces a bad path as an error rather than
// mounting an empty deck.
func TestLoadTapeFileMissingFile(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Skipf("emulator construction failed: %v", err)
	}
	if err := loadTapeFile(emu, filepath.Join(t.TempDir(), "nope.tap")); err == nil {
		t.Fatal("expected an error for a missing tape file")
	}
}
