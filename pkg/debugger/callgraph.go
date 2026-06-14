package debugger

import "sort"

// CallEdge is one (caller PC → callee PC) edge along with the
// number of times it was observed in a History snapshot. Used by
// the debugger's `callgraph` and `retgraph` commands to surface
// the function-call structure of tight loops where bare PC
// frequency (the `hot` command) is dominated by the inner fill /
// copy subroutine and obscures the outer caller's intent.
type CallEdge struct {
	From  uint16
	To    uint16
	Count int
}

// CallGraph aggregates history entries whose Source matches the
// supplied filter into (From → To) edges, sorted by descending
// count. Ties broken by (From asc, To asc) for deterministic
// output. Pass topN<=0 for the full edge list.
//
// Typical filters: SourceCall (function-call topology), SourceRet
// (return-site distribution — surfaces the call sites whose
// callees finish often vs. those that loop internally), SourceRst
// (RST handler entries).
//
// SourceFrom of zero in the history (no recorded caller) is
// surfaced as From=0; this happens for the very first entry of a
// ring and for entries whose branch source was cleared before
// recording.
func CallGraph(entries []HistoryEntry, filter PCSource, topN int) []CallEdge {
	if len(entries) == 0 {
		return nil
	}
	type key struct {
		from, to uint16
	}
	counts := make(map[key]int, len(entries)/8)
	for _, e := range entries {
		if e.Source != filter {
			continue
		}
		counts[key{e.SourceFrom, e.PC}]++
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]CallEdge, 0, len(counts))
	for k, c := range counts {
		out = append(out, CallEdge{From: k.from, To: k.to, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	if topN > 0 && topN < len(out) {
		out = out[:topN]
	}
	return out
}
