package rtc

import (
	"path/filepath"
	"testing"
)

// TestRTCNVRAMPersists verifies the battery-backed NVRAM survives across
// RTC instances via the persist path.
func TestRTCNVRAMPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i2c_nvram")

	r1 := New()
	r1.SetPersistPath(path)
	r1.Write(0x10, 0xAB)
	r1.Write(0x3F, 0xCD)
	if got := r1.Read(0x10); got != 0xAB {
		t.Fatalf("NVRAM[0x10] = %#x, want 0xAB", got)
	}

	r2 := New()
	r2.SetPersistPath(path)
	if got := r2.Read(0x10); got != 0xAB {
		t.Errorf("persisted NVRAM[0x10] = %#x, want 0xAB", got)
	}
	if got := r2.Read(0x3F); got != 0xCD {
		t.Errorf("persisted NVRAM[0x3F] = %#x, want 0xCD", got)
	}
}
