package debugger

import "testing"

// TestCallGraphFiltersBySource verifies that only entries matching
// the supplied PCSource contribute to edge counts.
func TestCallGraphFiltersBySource(t *testing.T) {
	entries := []HistoryEntry{
		{PC: 0x1000, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x1000, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x1000, SourceFrom: 0x4000, Source: SourceRet}, // not Call
		{PC: 0x1001, SourceFrom: 0x4000, Source: SourceJP},  // not Call
		{PC: 0x2000, SourceFrom: 0x5000, Source: SourceCall},
	}
	got := CallGraph(entries, SourceCall, 0)
	if len(got) != 2 {
		t.Fatalf("len(edges)=%d, want 2", len(got))
	}
	if got[0].From != 0x4000 || got[0].To != 0x1000 || got[0].Count != 2 {
		t.Errorf("top edge: got %+v, want From=$4000 To=$1000 Count=2", got[0])
	}
}

// TestCallGraphSortsByCountDesc locks the descending-count
// ordering — the top of the list must be the most-called edge.
func TestCallGraphSortsByCountDesc(t *testing.T) {
	entries := []HistoryEntry{
		{PC: 0x0100, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
	}
	got := CallGraph(entries, SourceCall, 0)
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	wantOrder := []struct {
		to    uint16
		count int
	}{{0x0200, 3}, {0x0300, 2}, {0x0100, 1}}
	for i, w := range wantOrder {
		if got[i].To != w.to || got[i].Count != w.count {
			t.Errorf("edge[%d]: got To=$%04X Count=%d, want To=$%04X Count=%d",
				i, got[i].To, got[i].Count, w.to, w.count)
		}
	}
}

// TestCallGraphTopNClips confirms the topN cap takes only the
// requested number of edges (highest-count ones).
func TestCallGraphTopNClips(t *testing.T) {
	entries := []HistoryEntry{
		{PC: 0x0100, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
	}
	got := CallGraph(entries, SourceCall, 2)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	if got[0].To != 0x0300 || got[1].To != 0x0200 {
		t.Errorf("top-2: got %v, want [$0300, $0200]",
			[2]uint16{got[0].To, got[1].To})
	}
}

// TestCallGraphTieBreakDeterministic confirms ties are broken by
// (From asc, To asc) so callers can rely on stable output.
func TestCallGraphTieBreakDeterministic(t *testing.T) {
	entries := []HistoryEntry{
		{PC: 0x0300, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceCall},
		{PC: 0x0100, SourceFrom: 0x4000, Source: SourceCall},
	}
	got := CallGraph(entries, SourceCall, 0)
	if got[0].To != 0x0100 || got[1].To != 0x0200 || got[2].To != 0x0300 {
		t.Errorf("tie-break order = [$%04X $%04X $%04X], want ascending",
			got[0].To, got[1].To, got[2].To)
	}
}

// TestCallGraphReturnsNilOnNoMatch — when no entry matches the
// filter we get nil, not an empty slice that callers have to
// special-case.
func TestCallGraphReturnsNilOnNoMatch(t *testing.T) {
	entries := []HistoryEntry{
		{PC: 0x0100, SourceFrom: 0x4000, Source: SourceJP},
		{PC: 0x0200, SourceFrom: 0x4000, Source: SourceJR},
	}
	got := CallGraph(entries, SourceCall, 0)
	if got != nil {
		t.Errorf("got %v, want nil for no-match", got)
	}
}
