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
			// PC $0008 is in MMU slot 0, left showing the default machine
			// ROM (no MMU override) — sram_pre_override(0)=1, so the gate
			// follows the rom3/altrom condition alone.
			if got := mem.DivMMCRom3Gate(0x0008); got != tc.want {
				t.Errorf("DivMMCRom3Gate() altrom=%#02x rom=%d = %v, want %v (zxnext.vhd:3138)", tc.altrom, tc.rom, got, tc.want)
			}
		})
	}
}

// TestDivMMCRom3GateSuppressedByMMURAM locks in the sram_pre_override(0)
// term of zxnext.vhd:3138 (override <= "110" at lines 3037-3043 when
// mmu_A21_A13(8)=0, i.e. the 8K slot covering the trap address maps a real
// RAM bank rather than the default machine ROM). A rom3-variant automap
// entry point must NOT engage when a program (e.g. Sonic) has mapped its
// own RAM over $0000-$3FFF via the MMU — the machine ROM isn't visible
// there, so the trap address belongs to the program, not the ROM/esxDOS.
func TestDivMMCRom3GateSuppressedByMMURAM(t *testing.T) {
	mem := newNextTestMemory(t)
	mem.SetAltROMReg(0x00)
	mem.SetROMBank(3) // rom3 selected → gate would otherwise be true

	// Default (no MMU override): slot 0 shows the machine ROM → gate true.
	if !mem.DivMMCRom3Gate(0x04D7) {
		t.Fatalf("baseline: gate should be true with rom3 selected and no MMU override")
	}

	// Map a RAM bank into MMU slot 0 ($0000-$1FFF) — sram_pre_override(0)=0.
	mem.SetMMU(0, 10) // bank 10 (not 0xFF=ROM)
	if mem.DivMMCRom3Gate(0x04D7) {
		t.Errorf("gate must be FALSE when MMU slot 0 maps RAM bank 10 (override(0)=0)")
	}
	// A trap address in slot 1 ($2000-$3FFF) is unaffected by slot 0's RAM.
	if !mem.DivMMCRom3Gate(0x2000) {
		t.Errorf("gate for a slot-1 address must follow slot 1 (still ROM here)")
	}
	// Map RAM into slot 1 too → slot-1 trap also suppressed.
	mem.SetMMU(1, 11)
	if mem.DivMMCRom3Gate(0x2000) {
		t.Errorf("gate must be FALSE when MMU slot 1 maps RAM bank 11")
	}
	// MMU set explicitly to ROM (0xFF) is the machine ROM → gate true again.
	mem.SetMMU(0, 0xFF)
	if !mem.DivMMCRom3Gate(0x04D7) {
		t.Errorf("gate must be true when MMU slot 0 is set to ROM (0xFF)")
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
