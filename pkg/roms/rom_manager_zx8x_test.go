package roms

import (
	"os"
	"path/filepath"
	"testing"
)

// The ZX81 ROM is 8 KB and the ZX80 ROM is 4 KB, unlike the 16 KB Spectrum
// system ROMs. LoadROM must accept those sizes for the new ROM types.
func TestLoadROMAcceptsZX8xSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zx81.rom"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zx80.rom"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	rm := NewROMManager(dir)

	if err := rm.LoadROM(ROMZX81, "zx81.rom"); err != nil {
		t.Errorf("LoadROM ZX81 (8 KB): %v", err)
	}
	if err := rm.LoadROM(ROMZX80, "zx80.rom"); err != nil {
		t.Errorf("LoadROM ZX80 (4 KB): %v", err)
	}
}

// LoadROMsForModel must resolve the ZX80/ZX81 mappings to their ROM files.
func TestLoadROMsForModelZX8x(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zx81.rom"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	rm := NewROMManager(dir)

	if err := rm.LoadROMsForModel(ModelZX81); err != nil {
		t.Fatalf("LoadROMsForModel(ModelZX81): %v", err)
	}
	rom, ok := rm.GetROM(ROMZX81)
	if !ok || len(rom) != 8192 {
		t.Errorf("GetROM(ROMZX81): ok=%v len=%d, want ok=true len=8192", ok, len(rom))
	}
}
