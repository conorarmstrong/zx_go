package divmmc

import (
	"testing"
)

// Entry-point gating tests.
//
// Verifies that the NR$B8 (entryPoints0) and NR$BB (entryPoints1)
// register bits correctly gate which PC values trigger the divMMC
// overlay paging-in. Per `divmmc.go::Step()`:
//
//   NR$B8 (entryPoints0):
//     bit 0 → enables PC=$0000  ("cold-start")
//     bit 1 → enables PC=$0008  ("RST 8 esxDOS API")
//     bit 7 → enables PC=$0038  ("IM 1 / RST $38")
//
//   NR$BB (entryPoints1):
//     bits 0:1 → enables PC=$0066 ("NMI" — any non-zero in 1:0)
//     bit 2 → enables PC=$04C6
//     bit 3 → enables PC=$0562
//     bit 4 → enables PC=$04D7
//     bit 5 → enables PC=$056A
//     bit 7 → enables PC=$3D00-$3DFF range
//
// Defaults: NR$B8=$83 (bits 0,1,7 — $0000/$0008/$0038), NR$BB=$CD
// (bits 0,2,3,6,7 — $0066/$04C6/$0562/[$3DXX]/divmmc-off-area-write).
// Verified against zxnext.vhd and the comments in WireDivMMCEntryPoints.

// newGatedPager builds a Pager with automap ON and explicit
// entryPoints values for gating tests. Sets NR$B9 (epValid0) to
// $FF (rom3 variant off) so gating tests for B8 only don't also
// have to account for B9.
//
// Tests that specifically exercise B9/BA gating set those fields
// directly after constructing.
func newGatedPager(ep0, ep1 byte) *Pager {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints0(ep0)
	p.SetEntryPoints1(ep1)
	p.SetEntryPointsValid0(0xFF)  // make B8 the sole gate for legacy tests
	p.SetEntryPointsTiming0(0xFF) // instant_on, so Step(trigger) pages in
	// this same M1 — isolates the entry-point gate from the
	// instant-vs-delayed timing (exercised by TestPagerDelayedTrigger).
	return p
}

// (makeROM helper lives in divmmc_test.go; this file uses it.)

// TestGate_PC0000_RequiresEP0Bit0 verifies that PC=$0000 only triggers
// paging in when NR$B8 bit 0 is set.
func TestGate_PC0000_RequiresEP0Bit0(t *testing.T) {
	// bit 0 clear: trigger does NOT fire
	p := newGatedPager(0xFE, 0xFF) // all bits except B8 bit 0
	p.Step(0x0000)
	if p.IsPagedIn() {
		t.Errorf("PC=$0000 with B8 bit 0 clear: paged in unexpectedly")
	}
	// bit 0 set: trigger fires
	p = newGatedPager(0x01, 0x00)
	p.Step(0x0000)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0000 with B8 bit 0 set: not paged in")
	}
}

// TestGate_PC0008_RequiresEP0Bit1 verifies PC=$0008 needs NR$B8 bit 1.
func TestGate_PC0008_RequiresEP0Bit1(t *testing.T) {
	p := newGatedPager(0xFD, 0xFF) // all except bit 1
	p.Step(0x0008)
	if p.IsPagedIn() {
		t.Errorf("PC=$0008 with B8 bit 1 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x02, 0x00)
	p.Step(0x0008)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0008 with B8 bit 1 set: not paged in")
	}
}

// TestGate_PC0038_RequiresEP0Bit7 verifies PC=$0038 needs NR$B8 bit 7.
func TestGate_PC0038_RequiresEP0Bit7(t *testing.T) {
	p := newGatedPager(0x7F, 0xFF) // all except bit 7
	p.Step(0x0038)
	if p.IsPagedIn() {
		t.Errorf("PC=$0038 with B8 bit 7 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x80, 0x00)
	p.Step(0x0038)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0038 with B8 bit 7 set: not paged in")
	}
}

// TestGate_PC0066_RequiresEP1Bits01 verifies PC=$0066 fires when
// EITHER bit 0 or bit 1 of NR$BB is set (= the "NMI any" gate).
func TestGate_PC0066_RequiresEP1Bits01(t *testing.T) {
	// both clear: no fire
	p := newGatedPager(0xFF, 0xFC) // all except bits 1:0
	p.Step(0x0066)
	if p.IsPagedIn() {
		t.Errorf("PC=$0066 with BB bits 1:0 both clear: paged in unexpectedly")
	}
	// bit 0 set + divMMC NMI asserted: fires
	p = newGatedPager(0x00, 0x01)
	p.AssertNMIButton()
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0066 with BB bit 0 set: not paged in")
	}
	// bit 1 set + divMMC NMI asserted: fires
	p = newGatedPager(0x00, 0x02)
	p.AssertNMIButton()
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0066 with BB bit 1 set: not paged in")
	}
}

// TestGate_PC0066_RequiresNMIButton locks in divmmc.vhd:120-121 — the
// $0066 NMI automap entry points are ANDed with button_nmi, so they fire
// ONLY while an actual divMMC NMI is asserted (i_divmmc_button → NR$02
// bit 2), not on a plain instruction fetch of $0066. A program that runs
// its own code through $0066 (e.g. Sonic's ISR) must NOT page the esxDOS
// NMI overlay in over it.
func TestGate_PC0066_RequiresNMIButton(t *testing.T) {
	// EP1 bit 1 set but no divMMC NMI asserted: $0066 must NOT page in.
	p := newGatedPager(0x00, 0x02)
	p.Step(0x0066)
	if p.IsPagedIn() {
		t.Errorf("$0066 fetched as regular code (no divMMC NMI): paged in (must require button_nmi)")
	}
	// With the divMMC NMI asserted (button_nmi), the trap fires.
	p = newGatedPager(0x00, 0x02)
	p.AssertNMIButton()
	p.Step(0x0066)
	if !p.IsPagedIn() {
		t.Errorf("$0066 with divMMC NMI asserted + BB bit 1: not paged in")
	}
}

// TestGate_PC04C6_RequiresEP1Bit2 verifies PC=$04C6 needs NR$BB bit 2.
func TestGate_PC04C6_RequiresEP1Bit2(t *testing.T) {
	p := newGatedPager(0xFF, 0xFB)
	p.Step(0x04C6)
	if p.IsPagedIn() {
		t.Errorf("PC=$04C6 with BB bit 2 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x00, 0x04)
	p.Step(0x04C6)
	p.Step(0x04C6 + 1) // rom3_delayed_on: overlay appears on the NEXT M1 (zxnext.vhd:2901)
	if !p.IsPagedIn() {
		t.Errorf("PC=$04C6 with BB bit 2 set: not paged in")
	}
}

// TestGate_PC0562_RequiresEP1Bit3 verifies PC=$0562 needs NR$BB bit 3.
func TestGate_PC0562_RequiresEP1Bit3(t *testing.T) {
	p := newGatedPager(0xFF, 0xF7)
	p.Step(0x0562)
	if p.IsPagedIn() {
		t.Errorf("PC=$0562 with BB bit 3 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x00, 0x08)
	p.Step(0x0562)
	p.Step(0x0562 + 1) // rom3_delayed_on: overlay appears on the NEXT M1 (zxnext.vhd:2901)
	if !p.IsPagedIn() {
		t.Errorf("PC=$0562 with BB bit 3 set: not paged in")
	}
}

// TestGate_PC04D7_RequiresEP1Bit4 verifies PC=$04D7 needs NR$BB bit 4.
func TestGate_PC04D7_RequiresEP1Bit4(t *testing.T) {
	p := newGatedPager(0xFF, 0xEF)
	p.Step(0x04D7)
	if p.IsPagedIn() {
		t.Errorf("PC=$04D7 with BB bit 4 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x00, 0x10)
	p.Step(0x04D7)
	p.Step(0x04D7 + 1) // rom3_delayed_on: overlay appears on the NEXT M1 (zxnext.vhd:2901)
	if !p.IsPagedIn() {
		t.Errorf("PC=$04D7 with BB bit 4 set: not paged in")
	}
}

// TestGate_PC056A_RequiresEP1Bit5 verifies PC=$056A needs NR$BB bit 5.
func TestGate_PC056A_RequiresEP1Bit5(t *testing.T) {
	p := newGatedPager(0xFF, 0xDF)
	p.Step(0x056A)
	if p.IsPagedIn() {
		t.Errorf("PC=$056A with BB bit 5 clear: paged in unexpectedly")
	}
	p = newGatedPager(0x00, 0x20)
	p.Step(0x056A)
	p.Step(0x056A + 1) // rom3_delayed_on: overlay appears on the NEXT M1 (zxnext.vhd:2901)
	if !p.IsPagedIn() {
		t.Errorf("PC=$056A with BB bit 5 set: not paged in")
	}
}

// TestGate_PC3DXXRange_RequiresEP1Bit7 verifies every PC in
// $3D00-$3DFF triggers paging when NR$BB bit 7 is set, and none
// when clear.
func TestGate_PC3DXXRange_RequiresEP1Bit7(t *testing.T) {
	// bit 7 clear: NO PC in range fires
	for pc := uint16(0x3D00); pc <= 0x3DFF; pc++ {
		p := newGatedPager(0x00, 0x7F)
		p.Step(pc)
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X with BB bit 7 clear: paged in unexpectedly", pc)
			return
		}
	}
	// bit 7 set: EVERY PC in range fires
	for pc := uint16(0x3D00); pc <= 0x3DFF; pc++ {
		p := newGatedPager(0x00, 0x80)
		p.Step(pc)
		if !p.IsPagedIn() {
			t.Errorf("PC=$%04X with BB bit 7 set: not paged in", pc)
			return
		}
	}
}

// TestGate_OutOfRangeNotTriggered verifies that PCs just outside
// $3D00-$3DFF don't trigger the bit-7 gate.
func TestGate_3DRangeBoundaries(t *testing.T) {
	cases := []struct {
		pc        uint16
		wantPaged bool
	}{
		{0x3CFF, false}, // just below
		{0x3D00, true},  // first in range
		{0x3DFF, true},  // last in range
		{0x3E00, false}, // just above
	}
	for _, c := range cases {
		p := newGatedPager(0x00, 0x80)
		p.Step(c.pc)
		if p.IsPagedIn() != c.wantPaged {
			t.Errorf("PC=$%04X with BB bit 7 set: pagedIn=%v, want %v",
				c.pc, p.IsPagedIn(), c.wantPaged)
		}
	}
}

// TestGate_AlreadyPagedIn_TriggerIsNoop verifies that once the
// overlay is paged in, subsequent trigger-PC fetches don't re-fire
// the page-in (per the "trigger only fires when overlay not already
// paged in" comment in Step()).
func TestGate_AlreadyPagedIn_TriggerIsNoop(t *testing.T) {
	p := newGatedPager(0xFF, 0xFF)
	p.Step(0x0000) // pages in
	if !p.IsPagedIn() {
		t.Fatal("setup: PC=$0000 should have paged in")
	}
	// Calling Step on another trigger PC should not toggle anything.
	p.Step(0x0038)
	if !p.IsPagedIn() {
		t.Errorf("subsequent trigger PC should leave overlay paged in")
	}
}

// TestGate_AutomapDisabled_NoTriggerFires verifies that with automap
// off, no trigger PC fires regardless of entry-point bits.
func TestGate_AutomapDisabled_NoTriggerFires(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(false)
	p.SetEntryPoints0(0xFF)
	p.SetEntryPoints1(0xFF)
	for _, pc := range TriggerPCs {
		p.Step(pc)
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X with automap off: paged in unexpectedly", pc)
			return
		}
	}
}

// TestGate_AllZeroEntryPoints_NoTriggerFires verifies that with both
// entry-point registers $00, no trigger PC fires even with automap on.
func TestGate_AllZeroEntryPoints_NoTriggerFires(t *testing.T) {
	p := newGatedPager(0x00, 0x00)
	for _, pc := range TriggerPCs {
		p.Step(pc)
		if p.IsPagedIn() {
			t.Errorf("PC=$%04X with EP=0/0: paged in unexpectedly", pc)
			return
		}
	}
}

// TestGate_DefaultEntryPoints_StandardTriggersFire verifies that with
// the documented soft-reset defaults (NR$B8=$83, NR$BB=$CD), the
// expected trigger PCs fire — covering the boot-time NextZXOS paths.
//
// Per WireDivMMCEntryPoints docs:
//
//	B8 = $83 = bits 0,1,7 → $0000/$0008/$0038 trigger
//	BB = $CD = bits 0,2,3,6,7 → $0066/$04C6/$0562 + [bit 7 = $3DXX]
//
// Expected to fire: $0000, $0008, $0038, $0066, $04C6, $0562, $3D00+
// Expected NOT to fire: $04D7 (bit 4), $056A (bit 5)
func TestGate_DefaultEntryPoints_StandardTriggersFire(t *testing.T) {
	type c struct {
		pc        uint16
		wantPaged bool
		name      string
	}
	for _, tc := range []c{
		{0x0000, true, "$0000 cold-start"},
		{0x0008, true, "$0008 RST 8"},
		{0x0038, true, "$0038 IM 1"},
		{0x0066, true, "$0066 NMI"},
		{0x04C6, true, "$04C6"},
		{0x0562, true, "$0562"},
		{0x3D00, true, "$3D00 (range start)"},
		{0x3D80, true, "$3D80 (range middle)"},
		{0x04D7, false, "$04D7 (BB bit 4 clear)"},
		{0x056A, false, "$056A (BB bit 5 clear)"},
	} {
		p := newGatedPager(0x83, 0xCD)
		if tc.pc == 0x0066 {
			p.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
		}
		p.Step(tc.pc)
		// $04C6/$0562 are rom3_delayed_on (zxnext.vhd:2901): the overlay
		// appears on the M1 AFTER the trigger, so drive one more fetch.
		p.Step(tc.pc + 1)
		if p.IsPagedIn() != tc.wantPaged {
			t.Errorf("%s: pagedIn=%v, want %v", tc.name, p.IsPagedIn(), tc.wantPaged)
		}
	}
}

// ===========================================================================
// NR$B9 (epValid0) + NR$BA (epTiming0) gating tests.
//
// VHDL zxnext.vhd:2891-2895:
//   divmmc_automap_instant_on    <= ep AND ep_valid AND ep_timing
//   divmmc_automap_delayed_on    <= ep AND ep_valid AND NOT ep_timing
//   divmmc_automap_rom3_instant  <= (ep AND NOT ep_valid AND ep_timing) OR …
//
// Effective per-RST page-in gate: B8[n] ALONE. B9[n]/BA[n] select WHICH
// of the four automap variants fires (normal vs ROM3, instant vs
// delayed), not WHETHER it fires — when B8[n]=1 exactly one of
// instant_on / delayed_on / rom3_instant_on / rom3_delayed_on is
// asserted. Our model collapses all four into a single "paged in"
// effect (we don't model the ROM3-image or instant/delayed distinction).
// ===========================================================================

// TestB8_PagesInRegardlessOfB9 verifies the VHDL-correct gate: with
// the construction defaults (B8=$83, B9=$01, BA=$00), ALL of RST $00,
// $08 and $38 page the overlay in — B9 only selects normal-ROM vs ROM3
// (here $0000 takes the normal path, $0008/$0038 take rom3_delayed_on),
// it does not gate the page-in.
//
// With the defaults BA=$00 every one of these is a *delayed_on* variant,
// so the page-in is effective on the NEXT M1 (not the trigger fetch) —
// we step a following non-trigger fetch and assert the overlay is in.
func TestB8_PagesInRegardlessOfB9(t *testing.T) {
	// Defaults: B8 = $83 (bits 0,1,7), B9 = $01, BA = $00.
	for _, pc := range []uint16{0x0000, 0x0008, 0x0038} {
		p := New(makeROM())
		p.SetAutomap(true)
		p.Step(pc)     // delayed_on: arms the page-in for the next M1
		p.Step(0x0100) // next M1: overlay pages in
		if !p.IsPagedIn() {
			t.Errorf("default B8=$83: RST $%04X should page in (B8[n]=1, B9 only picks ROM/ROM3)", pc)
		}
	}
}

// TestB9Gate_GuestWritesB9_EnablesAdditionalRSTs verifies that
// writing a non-default value to NR$B9 opens up the additional
// RST entry points (mimicking what TBBLUE.FW boot.bin / NextZXOS
// does once it's running).
func TestB9Gate_GuestWritesB9_EnablesAdditionalRSTs(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPointsValid0(0xFF) // open all RST validations

	for _, pc := range []uint16{0x0000, 0x0008, 0x0038} {
		p2 := New(makeROM())
		p2.SetAutomap(true)
		p2.SetEntryPointsValid0(0xFF)
		p2.Step(pc)     // BA=$00 default => delayed_on
		p2.Step(0x0100) // next M1: overlay pages in
		if !p2.IsPagedIn() {
			t.Errorf("B9 = $FF: RST $%04X should fire", pc)
		}
	}
}

// TestBAGate_AlternatePath_BAEnablesRSTWithoutB9 verifies the
// "rom3 instant on" path: B8[n] AND NOT B9[n] AND BA[n] should also
// page in (spec line 2899-2900). Our model collapses ROM3 vs ROM-0
// into a single bool, but the gate logic AND-OR is still relevant.
func TestBAGate_AlternatePath_BAEnablesRSTWithoutB9(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints0(0x02)       // B8 bit 1 = RST $08
	p.SetEntryPointsValid0(0x00)  // B9 bit 1 = 0 (instant path closed)
	p.SetEntryPointsTiming0(0x02) // BA bit 1 = 1 (rom3 path open)

	p.Step(0x0008)
	if !p.IsPagedIn() {
		t.Errorf("B8=$02 + B9=$00 + BA=$02: RST $08 should fire via BA path")
	}
}

// TestB8Set_B9BAClear_PagesInViaROM3 locks in the rom3_delayed_on case:
// B8[n]=1 with B9[n]=0 and BA[n]=0 still pages the overlay in, via the
// ROM3 delayed path (zxnext.vhd:2901). This is exactly NextZXOS's IM1
// configuration (B8 bit 7 set, B9=BA=0).
func TestB8Set_B9BAClear_PagesInViaROM3(t *testing.T) {
	for _, pc := range []uint16{0x0000, 0x0008, 0x0038} {
		p := New(makeROM())
		p.SetAutomap(true)
		p.SetEntryPoints0(0xFF)       // all RSTs enabled in B8
		p.SetEntryPointsValid0(0x00)  // B9 = 0 -> ROM3 path (not "no validation")
		p.SetEntryPointsTiming0(0x00) // BA = 0 -> delayed
		p.Step(pc)                    // rom3_delayed_on: arms page-in for next M1
		p.Step(0x0100)                // next M1: overlay pages in
		if !p.IsPagedIn() {
			t.Errorf("B8 set, B9=BA=0: RST $%04X should page in via rom3_delayed_on", pc)
		}
	}
}

// TestB9_DoesNotAffect_EntryPoints1 verifies that the B9/BA gate
// only applies to the RST entry points (NR$B8). The NR$BB-gated
// traps ($0066, $04C6, $0562, etc.) are independent and should
// still fire even when B9 = 0.
func TestB9_DoesNotAffect_EntryPoints1(t *testing.T) {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints1(0xFF)
	p.SetEntryPointsValid0(0x00) // disable all RST validation
	p.SetEntryPointsTiming0(0x00)

	for _, pc := range []uint16{0x0066, 0x04C6, 0x0562, 0x04D7, 0x056A, 0x3D00} {
		p2 := New(makeROM())
		p2.SetAutomap(true)
		p2.SetEntryPoints1(0xFF)
		p2.SetEntryPointsValid0(0x00)
		if pc == 0x0066 {
			p2.AssertNMIButton() // $0066 NMI automap requires button_nmi (divmmc.vhd:120)
		}
		p2.Step(pc)
		p2.Step(pc + 1) // $04C6/$0562/$04D7/$056A are delayed_on — overlay on next M1
		if !p2.IsPagedIn() {
			t.Errorf("PC $%04X (NR$BB-gated): should fire regardless of B9",
				pc)
		}
	}
}

// TestB9BA_DefaultValues_MatchSpec locks in the spec defaults so
// any accidental change to the New() initializer is caught.
func TestB9BA_DefaultValues_MatchSpec(t *testing.T) {
	p := New(makeROM())
	if got := p.EntryPointsValid0(); got != 0x01 {
		t.Errorf("default NR$B9 = $%02X, want $01 (per zxnext.vhd:5088)", got)
	}
	if got := p.EntryPointsTiming0(); got != 0x00 {
		t.Errorf("default NR$BA = $%02X, want $00 (per zxnext.vhd:5089)", got)
	}
}
