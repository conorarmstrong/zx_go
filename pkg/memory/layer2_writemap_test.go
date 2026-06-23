package memory

import "testing"

// Layer-2 write-paging (legacy port $123B). Per zxnext.vhd:3915-3933 a write
// to $123B with bit 4 clear sets: bit0=write-enable, bit2=read-enable,
// bit3=shadow, bits7:6=segment. When write-enable is set, CPU writes to the
// mapped region ($0000-$3FFF, or all 48K when segment=11) are redirected into
// Layer-2 RAM — 16K bank = activeBank(NR$12)/shadowBank(NR$13) + segment +
// offset — instead of the normal page. Sonic relies on this to clear its
// Layer-2 screen; without it the clear corrupts normal RAM (and its stack).
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
