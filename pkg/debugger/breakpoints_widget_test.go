package debugger

import "testing"

// countingBPStore wraps a BreakpointSet and counts List() calls, so
// tests can tell how many times the widget re-snapshots the store.
type countingBPStore struct {
	*BreakpointSet
	listCalls int
}

func (s *countingBPStore) List() map[uint16]BPEntry {
	s.listCalls++
	return s.BreakpointSet.List()
}

// TestBreakpointsWidget_RowTextUsesCachedSnapshot guards against
// re-copying the whole store (BreakpointSet.List does a full
// atomic-load + map copy) once per visible row on every list redraw;
// rowText must read from the snapshot Refresh() already took.
func TestBreakpointsWidget_RowTextUsesCachedSnapshot(t *testing.T) {
	base := NewBreakpointSet()
	base.Add(0x1000, BPEntry{Bank: -1})
	base.Add(0x2000, BPEntry{Bank: 1})
	base.Add(0x3000, BPEntry{Bank: 2, HasCond: true, Source: "a == $42"})
	store := &countingBPStore{BreakpointSet: base}

	w := NewBreakpointsWidget(store)
	w.Refresh()
	store.listCalls = 0 // isolate: only measure the row-render calls below

	for i := range w.keys {
		got := w.rowText(i)
		want := base.List()[w.keys[i]].FormatLine(w.keys[i])
		if got != want {
			t.Errorf("rowText(%d) = %q, want %q", i, got, want)
		}
	}
	if store.listCalls != 0 {
		t.Fatalf("rowText() called store.List() %d times across %d rows, want 0 (should reuse the Refresh snapshot)", store.listCalls, len(w.keys))
	}
}

// TestBreakpointsWidget_RowTextOutOfRange guards the bounds check
// list.CreateItem's pooling can trigger (Fyne reuses row templates
// past the live item count while the list is shrinking).
func TestBreakpointsWidget_RowTextOutOfRange(t *testing.T) {
	base := NewBreakpointSet()
	base.Add(0x1000, BPEntry{Bank: -1})
	w := NewBreakpointsWidget(base)
	w.Refresh()

	if got := w.rowText(len(w.keys)); got != "" {
		t.Fatalf("rowText(out of range) = %q, want empty", got)
	}
}
