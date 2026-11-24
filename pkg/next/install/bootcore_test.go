package install

import (
	"errors"
	"testing"
)

// ZX_GO_NEXT_CORE=3.02.05 selects that core's boot loader and its
// $0000-$1FFF boot ROM window; the default is 3.02.03's loader and window.
func TestBootCore(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_CORE", "")
	if l, low := BootCore(); l != FPGABootROM || low {
		t.Errorf("default: %q low=%v, want %q wide", l, low, FPGABootROM)
	}
	t.Setenv("ZX_GO_NEXT_CORE", "3.02.05")
	l, low := BootCore()
	if l != FPGABootROM30205 || !low {
		t.Errorf("3.02.05: %q low=%v, want %q narrow", l, low, FPGABootROM30205)
	}
	// Not bundled: with nothing installed it is reported missing, and the
	// caller falls back to a direct boot rather than a mismatched loader.
	t.Setenv("ZX_GO_NEXT_ROM_DIR", t.TempDir())
	if _, err := LoadROM(l); !errors.Is(err, ErrROMNotInstalled) {
		t.Errorf("uninstalled 3.02.05 loader: err = %v, want ErrROMNotInstalled", err)
	}
}
