package next

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func wireP3TestROMs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 0x4000), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWireContentionDisableBitSetDisablesContention(t *testing.T) {
	installtest.RedirectConfig(t)
	mem, err := memory.New(wireP3TestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireContentionDisable(disp, mem)

	if mem.RAMContentionDisabled {
		t.Errorf("precondition: RAMContentionDisabled should default to false")
	}

	// Per zxnext.vhd:5176, contention disable is bit 6 ($40), not
	// bit 1. Bit 1 ($02) is PSG TurboSound enable.
	disp.Select(0x08)
	disp.WriteData(0x40) // bit 6 = disable contention
	if !mem.RAMContentionDisabled {
		t.Errorf("after NextReg 0x08 = 0x40: RAMContentionDisabled should be true")
	}

	disp.WriteData(0x00) // clear
	if mem.RAMContentionDisabled {
		t.Errorf("after NextReg 0x08 = 0x00: RAMContentionDisabled should be false")
	}
}

func TestWireContentionDisableOtherBitsIgnored(t *testing.T) {
	installtest.RedirectConfig(t)
	mem, err := memory.New(wireP3TestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireContentionDisable(disp, mem)

	// Set every bit except bit 6 (contention disable per VHDL).
	disp.Select(0x08)
	disp.WriteData(0xBF) // 1011_1111 — all except bit 6
	if mem.RAMContentionDisabled {
		t.Errorf("bit 6 was not set; RAMContentionDisabled should stay false")
	}
}
