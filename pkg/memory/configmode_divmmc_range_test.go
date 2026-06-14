package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

type fakeDivMMC struct {
	ram [0x20000]byte // 128 KB, flat
}

func (f *fakeDivMMC) ReadRAM(idx int) byte            { return f.ram[idx&0x1FFFF] }
func (f *fakeDivMMC) WriteRAM(idx int, val byte)      { f.ram[idx&0x1FFFF] = val }
func (f *fakeDivMMC) ReadROMByte(addr int) byte       { return 0 }
func (f *fakeDivMMC) WriteROMByte(addr int, val byte) {}

// Config-mode RAMPAGE divMMC range — per TBBlue firmware hardware.h:
//
//	#define RAMPAGE_RAMDIVMMC 0x08 // 0x08..0x0F
//
// 8 × 16 KB pages = the full 128 KB divMMC RAM (16 × 8 KB banks,
// zxnext.vhd:4183 "16 pages of 8K = 128K allocated to divmmc").
// The dispatch previously stopped at $0B, silently dropping
// boot.bin module loads aimed at the upper 64 KB.
func TestConfigModeDivMMCRangeCoversAllEightPages(t *testing.T) {
	dir := "test_roms_cfg_divmmc_range"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := &fakeDivMMC{}
	mem.SetDivMMCRAM(d)
	mem.EnterConfigMode()

	for page := byte(0x08); page <= 0x0F; page++ {
		mem.SetConfigModeRAMPage(page)
		want := byte(0xA0 + page)
		mem.Write(0x0123, want)
		idx := int(page-0x08)*0x4000 + 0x0123
		if d.ram[idx] != want {
			t.Errorf("page $%02X: write did not land at divMMC idx %#x (got $%02X, want $%02X)",
				page, idx, d.ram[idx], want)
			continue
		}
		if got := mem.Read(0x0123); got != want {
			t.Errorf("page $%02X: read returned $%02X, want $%02X", page, got, want)
		}
	}
}
