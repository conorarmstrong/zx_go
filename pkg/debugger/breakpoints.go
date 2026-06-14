package debugger

import (
	"fmt"
	"sort"
	"strings"
)

// BPEntry is one breakpoint with optional ROM-bank filter and
// guard condition. Bank == -1 means "fire on any bank". HasCond
// gates Cond — without it the entry fires on every hit at PC.
//
// The same type is consumed by the telnet and visual debugger
// surfaces; both store entries by PC in a map[uint16]BPEntry.
type BPEntry struct {
	Bank    int // -1 = any
	HasCond bool
	Cond    Condition
	Source  string // raw user input for `if EXPR` — kept for display
	// Action is a semicolon-separated debugger command string set
	// via the telnet `do "..."` clause; run when the BP fires. Empty
	// for plain breakpoints and for ones set from the GUI.
	Action string
}

// FormatLine produces a one-line description suitable for the
// breakpoints list in either the telnet `list-breakpoints` output
// or the visual breakpoints panel.
func (e BPEntry) FormatLine(pc uint16) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "$%04X", pc)
	if e.Bank >= 0 {
		fmt.Fprintf(&sb, "  bank=%d", e.Bank)
	}
	if e.HasCond {
		fmt.Fprintf(&sb, "  if %s", e.Source)
	}
	return sb.String()
}

// SortedKeys returns the PCs in the map in ascending numerical
// order. Used by both surfaces to render a stable list.
func SortedKeys(m map[uint16]BPEntry) []uint16 {
	out := make([]uint16, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
