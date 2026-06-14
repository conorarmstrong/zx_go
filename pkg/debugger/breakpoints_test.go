package debugger

import "testing"

// iter 294: cover BPEntry.FormatLine + SortedKeys. Both are pure
// formatting helpers shared by the telnet + visual debuggers.

func TestBPEntry_FormatLine_PlainPC(t *testing.T) {
	e := BPEntry{Bank: -1}
	if got := e.FormatLine(0x8000); got != "$8000" {
		t.Errorf("FormatLine plain = %q, want $8000", got)
	}
}

func TestBPEntry_FormatLine_WithBank(t *testing.T) {
	e := BPEntry{Bank: 3}
	if got := e.FormatLine(0x4000); got != "$4000  bank=3" {
		t.Errorf("FormatLine bank = %q, want \"$4000  bank=3\"", got)
	}
}

func TestBPEntry_FormatLine_WithCondition(t *testing.T) {
	e := BPEntry{Bank: -1, HasCond: true, Source: "A == $42"}
	want := "$0100  if A == $42"
	if got := e.FormatLine(0x0100); got != want {
		t.Errorf("FormatLine cond = %q, want %q", got, want)
	}
}

func TestBPEntry_FormatLine_BankAndCondition(t *testing.T) {
	e := BPEntry{Bank: 7, HasCond: true, Source: "HL > $C000"}
	want := "$1234  bank=7  if HL > $C000"
	if got := e.FormatLine(0x1234); got != want {
		t.Errorf("FormatLine bank+cond = %q, want %q", got, want)
	}
}

func TestSortedKeys_AscendingOrder(t *testing.T) {
	m := map[uint16]BPEntry{
		0x8000: {Bank: -1},
		0x0100: {Bank: -1},
		0x4242: {Bank: -1},
		0xFFFF: {Bank: -1},
	}
	keys := SortedKeys(m)
	want := []uint16{0x0100, 0x4242, 0x8000, 0xFFFF}
	if len(keys) != len(want) {
		t.Fatalf("SortedKeys length = %d, want %d", len(keys), len(want))
	}
	for i := range keys {
		if keys[i] != want[i] {
			t.Errorf("SortedKeys[%d] = $%04X, want $%04X", i, keys[i], want[i])
		}
	}
}

func TestSortedKeys_Empty(t *testing.T) {
	if got := SortedKeys(map[uint16]BPEntry{}); len(got) != 0 {
		t.Errorf("SortedKeys empty = %v, want []", got)
	}
}
