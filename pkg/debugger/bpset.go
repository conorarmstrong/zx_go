package debugger

import (
	"sync"
	"sync/atomic"
)

// BreakpointSet is the single, shared breakpoint store used by BOTH
// the telnet (ZRCP) debugger and the visual GUI debugger, so a
// breakpoint set from one surface is immediately visible and active
// in the other (the emulator holds one instance and hands it to
// both).
//
// Reads are lock-free: the active map is published through an
// atomic.Pointer, so the CPU's BreakpointCheck hot path (every M1
// fetch) does a single atomic load + map index with no mutex.
// Mutations are copy-on-write under a small mutex — breakpoints
// change rarely, so the copy cost is irrelevant and the hot path
// stays contention-free.
//
// It implements the BreakpointsStore interface the GUI widget talks
// to (List/Add/Remove/Clear) and adds Lookup/Snapshot/Len for the
// telnet hot path and listings.
type BreakpointSet struct {
	mu  sync.Mutex
	ptr atomic.Pointer[map[uint16]BPEntry]
}

// NewBreakpointSet returns an empty shared breakpoint set.
func NewBreakpointSet() *BreakpointSet {
	s := &BreakpointSet{}
	empty := map[uint16]BPEntry{}
	s.ptr.Store(&empty)
	return s
}

// Lookup returns the entry at pc, lock-free. Safe to call from the
// CPU hot path.
func (s *BreakpointSet) Lookup(pc uint16) (BPEntry, bool) {
	m := s.ptr.Load()
	if m == nil {
		return BPEntry{}, false
	}
	e, ok := (*m)[pc]
	return e, ok
}

// Len reports the number of active breakpoints (lock-free).
func (s *BreakpointSet) Len() int {
	m := s.ptr.Load()
	if m == nil {
		return 0
	}
	return len(*m)
}

// Snapshot returns a copy of the current map for iteration/listing
// without holding the lock.
func (s *BreakpointSet) Snapshot() map[uint16]BPEntry {
	m := s.ptr.Load()
	if m == nil {
		return map[uint16]BPEntry{}
	}
	out := make(map[uint16]BPEntry, len(*m))
	for k, v := range *m {
		out[k] = v
	}
	return out
}

// List satisfies BreakpointsStore (alias of Snapshot).
func (s *BreakpointSet) List() map[uint16]BPEntry { return s.Snapshot() }

// Add inserts or replaces the breakpoint at pc (copy-on-write).
func (s *BreakpointSet) Add(pc uint16, e BPEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cloneLocked()
	next[pc] = e
	s.ptr.Store(&next)
}

// Remove deletes the breakpoint at pc, if any (copy-on-write).
func (s *BreakpointSet) Remove(pc uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cloneLocked()
	delete(next, pc)
	s.ptr.Store(&next)
}

// Clear removes all breakpoints.
func (s *BreakpointSet) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := map[uint16]BPEntry{}
	s.ptr.Store(&empty)
}

func (s *BreakpointSet) cloneLocked() map[uint16]BPEntry {
	cur := s.ptr.Load()
	next := make(map[uint16]BPEntry, len(*cur)+1)
	for k, v := range *cur {
		next[k] = v
	}
	return next
}
