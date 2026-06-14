package debugger

import "testing"

func TestPCHistogram_EmptyReturnsNil(t *testing.T) {
	if got := PCHistogram(nil, 10); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestPCHistogram_CountsAndSorts(t *testing.T) {
	in := []HistoryEntry{
		{PC: 0x1000}, {PC: 0x2000}, {PC: 0x1000}, {PC: 0x3000},
		{PC: 0x2000}, {PC: 0x1000},
	}
	got := PCHistogram(in, 0)
	want := []HotPC{
		{PC: 0x1000, Count: 3},
		{PC: 0x2000, Count: 2},
		{PC: 0x3000, Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestPCHistogram_TieBreaksByPC(t *testing.T) {
	in := []HistoryEntry{
		{PC: 0x3000}, {PC: 0x1000}, {PC: 0x2000},
		{PC: 0x3000}, {PC: 0x1000}, {PC: 0x2000},
	}
	got := PCHistogram(in, 0)
	// All tied at count 2; expected order: $1000, $2000, $3000.
	if got[0].PC != 0x1000 || got[1].PC != 0x2000 || got[2].PC != 0x3000 {
		t.Errorf("tie-break wrong: got %+v", got)
	}
}

func TestPCHistogram_TopNCaps(t *testing.T) {
	in := []HistoryEntry{
		{PC: 1}, {PC: 1}, {PC: 1},
		{PC: 2}, {PC: 2},
		{PC: 3},
	}
	got := PCHistogram(in, 2)
	if len(got) != 2 {
		t.Fatalf("topN=2 returned %d entries", len(got))
	}
	if got[0].PC != 1 || got[1].PC != 2 {
		t.Errorf("topN ordering wrong: %+v", got)
	}
}
