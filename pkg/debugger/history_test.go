package debugger

import "testing"

func TestHistoryPushAndLast(t *testing.T) {
	h := NewHistory(4)
	if h.Capacity() != 4 {
		t.Fatalf("Capacity = %d want 4", h.Capacity())
	}
	if h.Len() != 0 {
		t.Errorf("empty Len = %d want 0", h.Len())
	}
	for i := uint64(1); i <= 3; i++ {
		h.Push(HistoryEntry{PC: uint16(i * 0x100), Insns: i})
	}
	if h.Len() != 3 {
		t.Errorf("after 3 pushes Len = %d want 3", h.Len())
	}
	got := h.Last(10) // clamp to 3
	if len(got) != 3 {
		t.Fatalf("Last(10) = %d entries want 3", len(got))
	}
	for i, e := range got {
		if e.PC != uint16((i+1)*0x100) {
			t.Errorf("entry %d PC = $%04X want $%04X", i, e.PC, uint16((i+1)*0x100))
		}
	}
}

func TestHistoryWrapAroundKeepsRecent(t *testing.T) {
	h := NewHistory(3)
	// Push 5 entries — ring size is 3 — the oldest 2 should be
	// dropped and Last(3) returns the most-recent 3 in order.
	for i := uint64(1); i <= 5; i++ {
		h.Push(HistoryEntry{PC: uint16(i * 0x100), Insns: i})
	}
	if h.Len() != 3 {
		t.Errorf("Len = %d want 3", h.Len())
	}
	got := h.Last(3)
	wantPCs := []uint16{0x300, 0x400, 0x500}
	for i, want := range wantPCs {
		if got[i].PC != want {
			t.Errorf("entry %d PC = $%04X want $%04X", i, got[i].PC, want)
		}
	}
}

func TestHistoryLastNSmallerThanLen(t *testing.T) {
	h := NewHistory(8)
	for i := uint64(1); i <= 6; i++ {
		h.Push(HistoryEntry{PC: uint16(i * 0x100), Insns: i})
	}
	got := h.Last(3) // should be 0x400, 0x500, 0x600
	if len(got) != 3 {
		t.Fatalf("Last(3) = %d want 3", len(got))
	}
	for i, want := range []uint16{0x400, 0x500, 0x600} {
		if got[i].PC != want {
			t.Errorf("entry %d PC = $%04X want $%04X", i, got[i].PC, want)
		}
	}
}

func TestHistoryDisabled(t *testing.T) {
	h := NewHistory(0)
	h.Push(HistoryEntry{PC: 0x1234})
	if got := h.Last(1); got != nil {
		t.Errorf("Last on disabled history = %+v want nil", got)
	}
	if h.Len() != 0 || h.Capacity() != 0 {
		t.Errorf("disabled history: Len=%d Capacity=%d want 0 0", h.Len(), h.Capacity())
	}
}

func TestPackIFFIM(t *testing.T) {
	cases := []struct {
		iff1, iff2, halted bool
		im                 int
		want               byte
	}{
		{false, false, false, 0, 0x00},
		{true, true, false, 1, 0xC1},
		{false, false, true, 2, 0x22},
		{true, true, true, 1, 0xE1},
	}
	for _, c := range cases {
		got := PackIFFIM(c.iff1, c.iff2, c.halted, c.im)
		if got != c.want {
			t.Errorf("PackIFFIM(%v,%v,%v,%d) = $%02X want $%02X",
				c.iff1, c.iff2, c.halted, c.im, got, c.want)
		}
		// Round-trip via HistoryEntry accessors.
		e := HistoryEntry{IFFIM: got}
		if e.IFF1() != c.iff1 || e.IFF2() != c.iff2 || e.Halted() != c.halted || e.IM() != c.im {
			t.Errorf("round-trip lost bits for $%02X", got)
		}
	}
}

func TestHistoryClear(t *testing.T) {
	h := NewHistory(4)
	for i := uint64(1); i <= 6; i++ {
		h.Push(HistoryEntry{PC: uint16(i * 0x100)})
	}
	h.Clear()
	if h.Len() != 0 {
		t.Errorf("after Clear, Len = %d want 0", h.Len())
	}
	if got := h.Last(4); len(got) != 0 {
		t.Errorf("after Clear, Last(4) = %d want 0", len(got))
	}
}
