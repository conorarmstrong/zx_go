package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// ZX81: 8 KB ROM occupies $0000-$1FFF and is mirrored at $2000-$3FFF (A13 is
// not decoded); 16 KB RAM lives at $4000-$7FFF and is mirrored into the upper
// 32 KB because A15 is used only by the ULA, not for memory decode. ROM is
// read-only.
func TestSetupZX81MemoryMap(t *testing.T) {
	dir := t.TempDir()
	rom := make([]byte, 8192)
	rom[0x0000] = 0xAA
	rom[0x1FFF] = 0xBB
	if err := os.WriteFile(filepath.Join(dir, "zx81.rom"), rom, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(dir, roms.ModelZX81)
	if err != nil {
		t.Fatalf("New(ModelZX81): %v", err)
	}

	checks := []struct {
		addr uint16
		want byte
		what string
	}{
		{0x0000, 0xAA, "ROM start"},
		{0x1FFF, 0xBB, "ROM end"},
		{0x2000, 0xAA, "ROM mirror at $2000"},
		{0x3FFF, 0xBB, "ROM mirror at $3FFF"},
	}
	for _, c := range checks {
		if got := m.Read(c.addr); got != c.want {
			t.Errorf("%s: Read(%04X)=%02X, want %02X", c.what, c.addr, got, c.want)
		}
	}

	m.Write(0x4000, 0x42)
	if got := m.Read(0x4000); got != 0x42 {
		t.Errorf("RAM write/read: Read(0x4000)=%02X, want 0x42", got)
	}
	if got := m.Read(0xC000); got != 0x42 {
		t.Errorf("RAM A15 mirror: Read(0xC000)=%02X, want 0x42", got)
	}

	m.Write(0x0000, 0x99)
	if got := m.Read(0x0000); got != 0xAA {
		t.Errorf("ROM must be read-only: Read(0x0000)=%02X, want 0xAA", got)
	}
}

// ZX80: 4 KB ROM mirrored four times across $0000-$3FFF; RAM at $4000.
func TestSetupZX80MemoryMap(t *testing.T) {
	dir := t.TempDir()
	rom := make([]byte, 4096)
	rom[0x0000] = 0xC0
	rom[0x0FFF] = 0xD0
	if err := os.WriteFile(filepath.Join(dir, "zx80.rom"), rom, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(dir, roms.ModelZX80)
	if err != nil {
		t.Fatalf("New(ModelZX80): %v", err)
	}

	if got := m.Read(0x0000); got != 0xC0 {
		t.Errorf("ROM start: Read(0x0000)=%02X, want 0xC0", got)
	}
	if got := m.Read(0x1000); got != 0xC0 {
		t.Errorf("4K ROM mirror: Read(0x1000)=%02X, want 0xC0", got)
	}
	if got := m.Read(0x0FFF); got != 0xD0 {
		t.Errorf("ROM end: Read(0x0FFF)=%02X, want 0xD0", got)
	}
	m.Write(0x4000, 0x55)
	if got := m.Read(0x4000); got != 0x55 {
		t.Errorf("RAM write/read: Read(0x4000)=%02X, want 0x55", got)
	}
}
