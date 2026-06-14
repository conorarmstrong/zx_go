package memory

import "testing"

func TestBankSizeIsHalfPageSize(t *testing.T) {
	if BankSize*BanksPer16KPage != PageSize {
		t.Fatalf("BankSize*BanksPer16KPage = %d, want %d (PageSize)",
			BankSize*BanksPer16KPage, PageSize)
	}
	if BankSize != 0x2000 {
		t.Fatalf("BankSize = %#x, want 0x2000", BankSize)
	}
}

func TestSlotIndex(t *testing.T) {
	cases := []struct {
		addr uint16
		want int
	}{
		{0x0000, 0},
		{0x1FFF, 0},
		{0x2000, 1},
		{0x3FFF, 1},
		{0x4000, 2},
		{0x5FFF, 2},
		{0x6000, 3},
		{0x8000, 4},
		{0xA000, 5},
		{0xC000, 6},
		{0xE000, 7},
		{0xFFFF, 7},
	}
	for _, c := range cases {
		if got := SlotIndex(c.addr); got != c.want {
			t.Errorf("SlotIndex(%#x) = %d, want %d", c.addr, got, c.want)
		}
	}
}

func TestSlotOffset(t *testing.T) {
	cases := []struct {
		addr uint16
		want uint16
	}{
		{0x0000, 0x0000},
		{0x1FFF, 0x1FFF},
		{0x2000, 0x0000},
		{0x3FFF, 0x1FFF},
		{0x4000, 0x0000},
		{0xFFFF, 0x1FFF},
	}
	for _, c := range cases {
		if got := SlotOffset(c.addr); got != c.want {
			t.Errorf("SlotOffset(%#x) = %#x, want %#x", c.addr, got, c.want)
		}
	}
}
