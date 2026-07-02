package divmmc

import "testing"

// EP1 ROM3-gated delayed automap tests, exercising the NextZXOS
// cold-boot path.
//
// Per zxnext.vhd 2901-2905 the four NR$BB code entry points feed
// divmmc_automap_rom3_delayed_on — and ONLY that signal:
//
//	divmmc_automap_rom3_delayed_on <= (rst stuff) or
//	   (port_04xx_msb and port_c6_lsb and nr_bb_divmmc_ep_1(2)) or  -- $04C6
//	   (port_05xx_msb and port_62_lsb and nr_bb_divmmc_ep_1(3)) or  -- $0562
//	   (port_04xx_msb and port_d7_lsb and nr_bb_divmmc_ep_1(4)) or  -- $04D7
//	   (port_05xx_msb and port_6a_lsb and nr_bb_divmmc_ep_1(5));    -- $056A
//
// and per divmmc.vhd:130 the rom3_* signals only act when
// i_automap_rom3_active = 1, i.e. when ROM3 (the 48K ROM) is the
// currently selected machine ROM. Two consequences this file pins down:
//
//  1. ROM3 GATE: with any other ROM paged (ROM0/1/2) these traps are
//     complete no-ops. NextZXOS depends on this: its +3DOS code in
//     ROM2 executes ITS OWN instructions at $056A/$04C6/… as plain
//     code. Trapping them hijacks the DOS mid-mount into the esxDOS
//     tape loader (observed: ROM2 $056A `jp m` replaced by esxDOS
//     `ret` → tape-trap path → volume-label cluster-0 read $1009 →
//     NR$02 soft-reset loop, 641×/8000 frames).
//
//  2. DELAYED TIMING: even with ROM3 paged the automap is the
//     delayed_on variant — the trigger instruction itself is fetched
//     from the pre-overlay map; the overlay pages in on the NEXT M1.
var ep1ROM3Traps = []struct {
	pc  uint16
	bit byte
}{
	{0x04C6, 0x04},
	{0x0562, 0x08},
	{0x04D7, 0x10},
	{0x056A, 0x20},
}

func TestEP1Traps_ROM3Gated_NoAutomapOutsideROM3(t *testing.T) {
	for _, tc := range ep1ROM3Traps {
		p := newGatedPager(0x00, tc.bit)
		p.SetRom3Query(func(uint16) bool { return false }) // ROM2 (+3DOS) paged
		p.Step(tc.pc)
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X with ROM3 not selected: paged in (must be a no-op)", tc.pc)
		}
		p.Step(tc.pc + 1) // and nothing pending either
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X with ROM3 not selected: delayed page-in armed (must be a no-op)", tc.pc)
		}
	}
}

func TestEP1Traps_ROM3Delayed_TriggerM1RunsUnpaged(t *testing.T) {
	for _, tc := range ep1ROM3Traps {
		p := newGatedPager(0x00, tc.bit)
		p.SetRom3Query(func(uint16) bool { return true }) // ROM3 (48K) paged
		p.Step(tc.pc)
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X rom3_delayed_on: paged in on the trigger M1 (must be delayed)", tc.pc)
		}
		p.Step(tc.pc + 1)
		if !p.IsPagedIn() {
			t.Errorf("PC=$%04X rom3_delayed_on: not paged in on the following M1", tc.pc)
		}
	}
}
