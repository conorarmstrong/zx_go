package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The boot path defaults to core 3.02.05, the newest public core, whose boot
// loader was rebuilt for NextZXOS 2.10, whenever that loader is installed; it
// is not bundled, so without it the bundled 3.02.03 loader runs.
// ZX_GO_NEXT_CORE forces either.
func TestBootCore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZX_GO_NEXT_ROM_DIR", dir)
	t.Setenv("ZX_GO_NEXT_CORE", "")
	if l, low := BootCore(); l != FPGABootROM || low {
		t.Errorf("nothing installed: %q low=%v, want %q wide", l, low, FPGABootROM)
	}
	if err := os.WriteFile(filepath.Join(dir, FPGABootROM30205), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	if l, low := BootCore(); l != FPGABootROM30205 || !low {
		t.Errorf("3.02.05 installed: %q low=%v, want %q narrow", l, low, FPGABootROM30205)
	}
	t.Setenv("ZX_GO_NEXT_CORE", "3.02.03")
	if l, low := BootCore(); l != FPGABootROM || low {
		t.Errorf("forced 3.02.03: %q low=%v, want %q wide", l, low, FPGABootROM)
	}
	t.Setenv("ZX_GO_NEXT_CORE", "3.02.05")
	if err := os.Remove(filepath.Join(dir, FPGABootROM30205)); err != nil {
		t.Fatal(err)
	}
	l, low := BootCore()
	if l != FPGABootROM30205 || !low {
		t.Errorf("forced 3.02.05: %q low=%v, want %q narrow", l, low, FPGABootROM30205)
	}
	if _, err := LoadROM(l); !errors.Is(err, ErrROMNotInstalled) {
		t.Errorf("forced but not installed: err = %v, want ErrROMNotInstalled", err)
	}
}
