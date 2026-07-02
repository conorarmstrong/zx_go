package memory

import "testing"

// Layer-2 write-paging (legacy port $123B). Per zxnext.vhd:3915-3933 a write
// to $123B with bit 4 clear sets: bit0=write-enable, bit2=read-enable,
// bit3=shadow, bits7:6=segment. When write-enable is set, CPU writes to the
// mapped region ($0000-$3FFF, or $0000-$BFFF when segment=11 — $C000-$FFFF
// is never mapped this way) are redirected into Layer-2 RAM — 16K bank =
// activeBank(NR$12)/shadowBank(NR$13) + segment + offset — instead of the
// normal page. Sonic relies on this to clear its Layer-2 screen; without it
// the clear corrupts normal RAM (and its stack).
func TestLayer2WriteMapRedirectsWrites(t *testing.T) {
	m := newNextTestMemory(t)
	m.SetLayer2ActiveBank(20)
	m.SetLayer2ShadowBank(40)

	// Write-enable, segment 0 ($123B = 0x01): $1234 → bank 20, offset $1234.
	m.SetLayer2MapControl(0x01)
	m.Write(0x1234, 0xAB)
	if got := m.GetPage(20)[0x1234]; got != 0xAB {
		t.Errorf("segment 0 write: GetPage(20)[$1234]=$%02X, want $AB", got)
	}

	// Segment 1 ($123B bit6 = 0x40 | wr_en 0x01): $1234 → bank 21.
	m.SetLayer2MapControl(0x41)
	m.Write(0x1234, 0xCD)
	if got := m.GetPage(21)[0x1234]; got != 0xCD {
		t.Errorf("segment 1 write: GetPage(21)[$1234]=$%02X, want $CD", got)
	}

	// Shadow ($123B bit3 = 0x08 | wr_en 0x01, segment 0): → shadowBank 40.
	m.SetLayer2MapControl(0x09)
	m.Write(0x0100, 0x5A)
	if got := m.GetPage(40)[0x0100]; got != 0x5A {
		t.Errorf("shadow write: GetPage(40)[$0100]=$%02X, want $5A", got)
	}

	// Write-disable ($123B = 0x00): writes must NOT go to Layer-2 RAM.
	m.SetLayer2MapControl(0x00)
	m.GetPage(20)[0x1234] = 0x00
	m.Write(0x1234, 0xEE)
	if got := m.GetPage(20)[0x1234]; got == 0xEE {
		t.Errorf("write-disabled: GetPage(20)[$1234]=$%02X — must not touch Layer-2 RAM", got)
	}
}

// TestLayer2WriteMapExcludesTopSlotInAllSegmentsMode verifies that segment 3
// ("map all segments") still excludes $C000-$FFFF. zxnext.vhd's sram_pre_override(1)
// term for the non-bottom-16K case is
// `((not cpu_a(15)) or (not cpu_a(14))) and segment(1) and segment(0)`,
// which is forced to 0 whenever cpu_a(15:14)="11" (i.e. addr >= $C000)
// regardless of the segment register — the legacy $123B map only ever
// reaches $0000-$BFFF, leaving the top 16K on the normal classic/MMU page.
func TestLayer2WriteMapExcludesTopSlotInAllSegmentsMode(t *testing.T) {
	m := newNextTestMemory(t)
	m.SetLayer2ActiveBank(20)

	// segment=3 (bits7:6=11) + write-enable: $123B = 0xC1.
	m.SetLayer2MapControl(0xC1)

	// $4000-$BFFF (segments 1/2) are redirected into Layer-2 banks 21/22.
	m.Write(0x4000, 0x11)
	if got := m.GetPage(21)[0x0000]; got != 0x11 {
		t.Errorf("segment 1 (all-segments mode): GetPage(21)[0]=$%02X, want $11", got)
	}
	m.Write(0x8000, 0x22)
	if got := m.GetPage(22)[0x0000]; got != 0x22 {
		t.Errorf("segment 2 (all-segments mode): GetPage(22)[0]=$%02X, want $22", got)
	}

	// $C000-$FFFF must NOT be redirected into Layer-2 bank 23 — it stays the
	// normal classic-paged RAM bank (bank 0 at reset).
	m.Write(0xC000, 0x33)
	if got := m.GetPage(23)[0x0000]; got == 0x33 {
		t.Errorf("$C000 wrongly redirected into Layer-2 bank 23")
	}
	if got := m.GetPage(0)[0x0000]; got != 0x33 {
		t.Errorf("$C000 write should land in classic RAM bank 0: got $%02X, want $33", got)
	}
}

// TestLayer2ReadMapRedirectsReads is the read-side counterpart of
// TestLayer2WriteMapRedirectsWrites: $123B bit 2 (read-enable) redirects CPU
// reads of the mapped region from Layer-2 RAM instead of the normal page.
func TestLayer2ReadMapRedirectsReads(t *testing.T) {
	m := newNextTestMemory(t)
	m.SetLayer2ActiveBank(20)
	m.GetPage(20)[0x1234] = 0x77

	// Read-enable only, segment 0: $123B = 0x04 (bit 2).
	m.SetLayer2MapControl(0x04)
	if got := m.Read(0x1234); got != 0x77 {
		t.Errorf("Read($1234) with l2RdEn = $%02X, want $77 (from Layer-2 bank 20)", got)
	}

	// Read-disable: must fall back to the normal ROM/RAM map.
	m.SetLayer2MapControl(0x00)
	if got := m.Read(0x1234); got == 0x77 {
		t.Errorf("Read($1234) with read-disabled still returned the Layer-2 byte $77")
	}
}
