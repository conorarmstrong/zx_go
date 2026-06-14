package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestDivMMCRom3Gate locks in zxnext.vhd:3138 — the rom3-variant divMMC
// automap entry points (NR$B9 bit CLEAR) engage per:
//
//	(sram_altrom_en and sram_pre_alt_128_n) or (sram_pre_rom3 and not sram_altrom_en)
//
// where, for the +3/Next personality (zxnext.vhd:2987-2996):
//
//	locks set:   alt_128_n = lock_rom1 (NR$8C bit 5)
//	locks clear: alt_128_n = port_1ffd_rom(0)  (ROM bank bit 0)
//	pre_rom3    = rom bank == 3
//
// and sram_altrom_en for an M1 FETCH (a read; zxnext.vhd:3078) requires
// NR$8C enable (bit 7) with read-replacement selected (bit 6 = 0).
// An automap trigger is a fetch, so the altrom arm only applies in
// read-replacement mode — write-only altrom ($C0) falls back to the
// plain rom3 arm.
func TestDivMMCRom3Gate(t *testing.T) {
	mem := newNextTestMemory(t)
	for _, tc := range []struct {
		name   string
		altrom byte
		rom    int
		want   bool
	}{
		{"altrom off, rom3", 0x00, 3, true},
		{"altrom off, rom0", 0x00, 0, false},
		{"altrom reads no locks, rom0 (bit0=0)", 0x80, 0, false},
		{"altrom reads no locks, rom1 (bit0=1)", 0x80, 1, true},
		{"altrom reads no locks, rom3 (bit0=1)", 0x80, 3, true},
		{"altrom reads lock_rom1, rom0", 0xA0, 0, true},
		{"altrom reads lock_rom0, rom0", 0x90, 0, false},
		{"altrom write-only, rom0", 0xC0, 0, false},
		{"altrom write-only, rom3 (falls back to rom3 arm)", 0xC0, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem.SetAltROMReg(tc.altrom)
			mem.SetROMBank(tc.rom)
			if got := mem.DivMMCRom3Gate(); got != tc.want {
				t.Errorf("DivMMCRom3Gate() altrom=%#02x rom=%d = %v, want %v (zxnext.vhd:3138)", tc.altrom, tc.rom, got, tc.want)
			}
		})
	}
}

func newNextTestMemory(t *testing.T) *Memory {
	t.Helper()
	mem, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	return mem
}
