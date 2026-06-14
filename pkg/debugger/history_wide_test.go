package debugger

import "testing"

func TestNewHistory_NarrowByDefault(t *testing.T) {
	h := NewHistory(8)
	if h.Wide() {
		t.Fatalf("plain NewHistory should not be wide")
	}
}

func TestNewHistoryWide_IsWide(t *testing.T) {
	h := NewHistoryWide(8)
	if !h.Wide() {
		t.Fatalf("NewHistoryWide should be wide")
	}
}

func TestHistory_WideEntriesRoundTrip(t *testing.T) {
	h := NewHistoryWide(4)
	h.Push(HistoryEntry{PC: 0x1FFA, IX: 0xFFFF, IY: 0xCCE7, HL: 0x07F7, BC: 0x2500, DE: 0x52BE})
	h.Push(HistoryEntry{PC: 0x1FFB, IX: 0x1234, IY: 0xCCE7, HL: 0x07F8, BC: 0x2500, DE: 0x52BE})
	got := h.Last(2)
	if len(got) != 2 {
		t.Fatalf("Last(2) returned %d entries", len(got))
	}
	if got[0].IX != 0xFFFF || got[1].IX != 0x1234 {
		t.Fatalf("IX field not preserved: got[0].IX=%04X got[1].IX=%04X", got[0].IX, got[1].IX)
	}
	if got[1].HL != 0x07F8 {
		t.Errorf("HL field not preserved")
	}
}
