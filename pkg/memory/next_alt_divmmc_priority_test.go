package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// divMMC-over-AltROM WRITE priority (the development log/D29).
//
// zxnext.vhd's final memory mux (3084-3130) tests the divMMC override
// FIRST: `sram_pre_override(2)='1' and divmmc_ram_en='1'` selects the
// divMMC RAM bank (writeable) BEFORE the `sram_altrom_en` branch is even
// considered — for writes as much as reads. Our read dispatch already
// encodes this (PeripheralRead consulted first, with the same citation);
// the WRITE dispatch had the Alt-ROM write-redirect (NR$8C bits 7|6)
// short-circuiting BEFORE PeripheralWrite, so with the overlay paged in
// AND NextZXOS's runtime ROM-patching redirect active, esxDOS window
// writes (e.g. the sector-cache seek field at $2335) were stolen into the
// alt-ROM buffer — the fetch saw the overlay, the write didn't, and the
// mount's root-dir seek was silently lost (the enAltZX.rom error screen).
// divMMC-over-AltROM READ priority (the development log).
//
// The same zxnext.vhd final mux (3084-3130) orders READS identically:
// divmmc_rom_en, then divmmc_ram_en, then layer2, then romcs, and only
// THEN sram_altrom_en. So with the overlay paged in (conmem or automap)
// AND NextZXOS's Alt-ROM read-redirect active (NR$8C bit 7 set, bit 6
// clear — the state the Browser launcher runs in), $0000-$3FFF fetches
// must come from the divMMC overlay, NOT the alt-ROM buffer. Our Read()
// had the redirect short-circuiting BEFORE PeripheralRead: the IM1
// wrapper's `out ($E3),$80` then fetched alt-ROM bytes ($00s) instead
// of the esxDOS ROM, NOP-slid through its own body, and the menu-item
// launch derailed (#255).
func TestDivMMCWindowRead_BeatsAltROMRedirect(t *testing.T) {
	m, err := New("../../roms", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Alt-ROM read redirect: bit 7 enable, bit 6 clear = reads come
	// from the alt-ROM buffer (when divMMC is out).
	m.SetAltROMReg(0x80)
	// Make the alt-ROM byte distinguishable.
	m.GetAltROM(0)[0x0048] = 0x55
	// Simulate the divMMC overlay paged in: PeripheralRead claims
	// bottom-16K reads, returning a marker byte.
	m.PeripheralRead = func(addr uint16) (byte, bool) {
		if addr < 0x4000 {
			return 0xE6, true // esxDOS ROM marker
		}
		return 0, false
	}
	if got := m.Read(0x0048); got != 0xE6 {
		t.Fatalf("Read($0048) = $%02X, want $E6 (divMMC overlay must beat the Alt-ROM read redirect, zxnext.vhd:3084-3130)", got)
	}
	// With the overlay OUT (peripheral declines), the redirect serves
	// the alt-ROM byte as before.
	m.PeripheralRead = func(addr uint16) (byte, bool) { return 0, false }
	if got := m.Read(0x0048); got != 0x55 {
		t.Fatalf("Read($0048) with overlay out = $%02X, want $55 (alt-ROM redirect)", got)
	}
}

func TestDivMMCWindowWrite_BeatsAltROMRedirect(t *testing.T) {
	m, err := New("../../roms", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Enable the Alt-ROM write redirect (NR$8C bit 7 = enable, bit 6 =
	// visible-during-writes) exactly as NextZXOS does at runtime.
	m.SetAltROMReg(0xC0)
	// Simulate the divMMC overlay paged in: PeripheralWrite claims the
	// window write.
	var got struct {
		addr uint16
		val  byte
		hits int
	}
	m.PeripheralWrite = func(addr uint16, val byte) bool {
		if addr >= 0x2000 && addr < 0x4000 {
			got.addr, got.val = addr, val
			got.hits++
			return true
		}
		return false
	}
	m.Write(0x2335, 0x18)
	if got.hits != 1 || got.addr != 0x2335 || got.val != 0x18 {
		t.Fatalf("divMMC window write stolen by Alt-ROM redirect: PeripheralWrite hits=%d addr=$%04X val=$%02X, want 1/$2335/$18",
			got.hits, got.addr, got.val)
	}
}
