package debugger

import "sort"

// HotPC is one PC + the number of times it appears in a History
// snapshot. Used by the debugger's `hot` command to surface the
// tightest loops in the M1 ring.
type HotPC struct {
	PC    uint16
	Count int
}

// PCHistogram tallies PC occurrences in the supplied entries and
// returns them sorted by descending count. Ties are broken by PC
// ascending for deterministic output. Pass topN<=0 to get the full
// histogram.
func PCHistogram(entries []HistoryEntry, topN int) []HotPC {
	if len(entries) == 0 {
		return nil
	}
	counts := make(map[uint16]int, len(entries)/4)
	for _, e := range entries {
		counts[e.PC]++
	}
	out := make([]HotPC, 0, len(counts))
	for pc, c := range counts {
		out = append(out, HotPC{PC: pc, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].PC < out[j].PC
	})
	if topN > 0 && topN < len(out) {
		out = out[:topN]
	}
	return out
}
