package debugger

import (
	"fmt"
	"sync"
)

// RegWatch is a single register-change breakpoint. The watch fires
// when the named register's value transitions:
//
//   - if neither HasFrom nor HasTo is set, fire on any change;
//   - if only HasTo, fire when the new value equals To;
//   - if only HasFrom, fire when the old value equals From;
//   - if both, fire when old == From and new == To.
//
// The semantic distinction matters for hard-bug investigations
// where the question is "what set IFF1 from 0 to 1" (HasFrom +
// HasTo) versus "halt as soon as IX ever changes" (neither).
type RegWatch struct {
	Reg     string
	HasFrom bool
	From    int64
	HasTo   bool
	To      int64

	primed bool
	last   int64
}

// FormatLine produces a one-line description suitable for the
// list-watches / visual panel.
func (w RegWatch) FormatLine() string {
	switch {
	case w.HasFrom && w.HasTo:
		return fmt.Sprintf("%s from $%X -> %d", w.Reg, w.From, w.To)
	case w.HasFrom:
		return fmt.Sprintf("%s from $%X", w.Reg, w.From)
	case w.HasTo:
		return fmt.Sprintf("%s -> %d", w.Reg, w.To)
	default:
		return fmt.Sprintf("%s (any change)", w.Reg)
	}
}

// RegWatchSet is a thread-safe collection of register watches.
// Check is invoked from the CPU pre-fetch hook; Add / Remove / List
// run from debugger commands. Each watch carries its own "last
// observed value", so the set safely accumulates watches at
// arbitrary times.
type RegWatchSet struct {
	mu      sync.Mutex
	watches map[string]*RegWatch
	order   []string // for stable iteration
}

// NewRegWatchSet returns an empty watch set.
func NewRegWatchSet() *RegWatchSet {
	return &RegWatchSet{watches: map[string]*RegWatch{}}
}

// Add inserts or replaces a watch keyed by Reg.
func (s *RegWatchSet) Add(w RegWatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, existed := s.watches[w.Reg]; !existed {
		s.order = append(s.order, w.Reg)
	}
	// Reset prime state so the new watch baselines off the next Check.
	w.primed = false
	wcopy := w
	s.watches[w.Reg] = &wcopy
}

// Remove deletes the watch on the given register. No-op if absent.
func (s *RegWatchSet) Remove(reg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.watches[reg]; !ok {
		return
	}
	delete(s.watches, reg)
	for i, r := range s.order {
		if r == reg {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// Clear removes every watch.
func (s *RegWatchSet) Clear() {
	s.mu.Lock()
	s.watches = map[string]*RegWatch{}
	s.order = nil
	s.mu.Unlock()
}

// List returns a snapshot of the current watches in insertion order.
func (s *RegWatchSet) List() []RegWatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RegWatch, 0, len(s.order))
	for _, r := range s.order {
		if w, ok := s.watches[r]; ok {
			out = append(out, *w)
		}
	}
	return out
}

// Empty reports whether the set has no active watches. Hot-path
// helper so the pre-fetch hook can skip the State allocation when
// there's nothing to do.
func (s *RegWatchSet) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.watches) == 0
}

// Check evaluates every watch against the supplied State and
// returns true if any of them should halt the CPU. The watch's
// last-observed value is updated regardless of whether it fired.
//
// First Check after Add is treated as "prime": no fire even if the
// value already matches the To condition. This stops a fresh
// `watch-reg iff1 to 0` (with IFF1 already at 0 from the soft reset)
// from instantly halting.
func (s *RegWatchSet) Check(src State) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	fired := false
	for _, w := range s.watches {
		cur, ok := src.Reg(w.Reg)
		if !ok {
			continue
		}
		if !w.primed {
			w.last = cur
			w.primed = true
			continue
		}
		if cur == w.last {
			continue
		}
		// State transitioned. Decide whether THIS transition matches
		// the predicate.
		match := true
		if w.HasFrom && w.last != w.From {
			match = false
		}
		if w.HasTo && cur != w.To {
			match = false
		}
		w.last = cur
		if match {
			fired = true
		}
	}
	return fired
}
