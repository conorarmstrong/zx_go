package if2

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempROM writes a file of the given size to a fresh temp
// directory and returns its path. The content is the byte value
// (addr & 0xFF) at each offset so tests can verify both size and
// byte-level integrity.
func writeTempROM(t *testing.T, name string, size int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write temp rom: %v", err)
	}
	return path
}

func TestLoadValidCartridge(t *testing.T) {
	path := writeTempROM(t, "jetpac.rom", CartridgeSize)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Name != "jetpac" {
		t.Errorf("Name = %q, want %q", c.Name, "jetpac")
	}
	// Spot-check a few bytes: the synthetic cartridge has
	// data[i] = i & 0xFF.
	for _, addr := range []uint16{0, 1, 0xFF, 0x100, 0x3FFF} {
		got := c.Read(addr)
		want := byte(addr & 0xFF)
		if got != want {
			t.Errorf("Read(0x%04X) = 0x%02X, want 0x%02X", addr, got, want)
		}
	}
}

func TestLoadRejectsWrongSize(t *testing.T) {
	cases := []int{0, 1, CartridgeSize - 1, CartridgeSize + 1, 2 * CartridgeSize}
	for _, size := range cases {
		path := writeTempROM(t, "bogus.rom", size)
		if _, err := Load(path); err == nil {
			t.Errorf("Load(%d-byte file) returned nil error, want size-validation failure", size)
		}
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.rom")
	if _, err := Load(path); err == nil {
		t.Error("Load of missing file returned nil error, want wrapped os error")
	}
}

func TestReadWrapsOutOfRange(t *testing.T) {
	path := writeTempROM(t, "rom", CartridgeSize)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Addresses above the cartridge window wrap modulo the
	// cartridge size (the Read method masks with CartridgeSize-1
	// for BCE-friendly indexing). The test synthesised data has
	// data[i] = i & 0xFF, so wrapped reads should match that.
	for _, addr := range []uint16{0x4000, 0x4001, 0x8000, 0xFFFF} {
		want := byte(addr & 0xFF)
		if got := c.Read(addr); got != want {
			t.Errorf("Read(0x%04X) = 0x%02X, want 0x%02X (wrapped)", addr, got, want)
		}
	}
}

func TestNameStripsExtension(t *testing.T) {
	cases := map[string]string{
		"jetpac.rom":        "jetpac",
		"chess.ROM":         "chess",
		"horace":            "horace",
		"space-raiders.rom": "space-raiders",
	}
	for filename, wantName := range cases {
		path := writeTempROM(t, filename, CartridgeSize)
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", filename, err)
		}
		if c.Name != wantName {
			t.Errorf("Name for %q = %q, want %q", filename, c.Name, wantName)
		}
	}
}
