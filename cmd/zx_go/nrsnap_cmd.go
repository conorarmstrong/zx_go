package main

import (
	"fmt"
	"strings"
	"sync"
)

// nrSnapshot is a 256-byte capture of the entire NextReg file at
// a named instant. The remote debugger keeps an ordered map of
// these so `nr-diff` can compare any two by name.
type nrSnapshot struct {
	regs [256]byte
}

// nrSnapStore holds named snapshots. Internal mutex; commands run
// under their own lock plus this one, but contention is trivial
// (a few snaps per investigation).
type nrSnapStore struct {
	mu    sync.Mutex
	snaps map[string]*nrSnapshot
	order []string
}

func (s *nrSnapStore) capture(name string, regs [256]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snaps == nil {
		s.snaps = map[string]*nrSnapshot{}
	}
	if _, existed := s.snaps[name]; !existed {
		s.order = append(s.order, name)
	}
	s.snaps[name] = &nrSnapshot{regs: regs}
}

func (s *nrSnapStore) get(name string) *nrSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snaps[name]
}

func (s *nrSnapStore) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

func (s *nrSnapStore) lastTwo() (a, b string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) >= 2 {
		return s.order[len(s.order)-2], s.order[len(s.order)-1]
	}
	return "", ""
}

func (s *nrSnapStore) clear(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		s.snaps = nil
		s.order = nil
		return true
	}
	if _, ok := s.snaps[name]; !ok {
		return false
	}
	delete(s.snaps, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true
}

// cmdNRSnap captures all 256 NextRegs into a named slot.
//
//	nr-snap NAME       capture under NAME
//	nr-snap            list active snapshots
//	nr-snap clear      drop all
//	nr-snap clear NAME drop one
func (d *remoteDebugger) cmdNRSnap(args []string) string {
	if d.emu.nextRegs == nil {
		return "ERR no nextregs (not a Next model)"
	}
	if len(args) == 0 {
		names := d.nrSnaps.list()
		if len(names) == 0 {
			return "OK (no snapshots)"
		}
		return "OK snapshots: " + strings.Join(names, ", ")
	}
	if strings.ToLower(args[0]) == "clear" {
		if len(args) >= 2 {
			if d.nrSnaps.clear(args[1]) {
				return "OK removed " + args[1]
			}
			return "ERR snapshot not found: " + args[1]
		}
		d.nrSnaps.clear("")
		return "OK all snapshots cleared"
	}
	name := args[0]
	var regs [256]byte
	for i := 0; i < 256; i++ {
		regs[i] = d.emu.nextRegs.Raw(byte(i))
	}
	d.nrSnaps.capture(name, regs)
	return "OK captured " + name
}

// cmdNRDiff reports register-by-register differences between two
// snapshots.
//
//	nr-diff            compare last two captured
//	nr-diff A          compare A against the current live state
//	nr-diff A B        compare A against B
func (d *remoteDebugger) cmdNRDiff(args []string) string {
	if d.emu.nextRegs == nil {
		return "ERR no nextregs (not a Next model)"
	}
	var a, b *nrSnapshot
	var nameA, nameB string
	switch len(args) {
	case 0:
		nameA, nameB = d.nrSnaps.lastTwo()
		if nameA == "" || nameB == "" {
			return "ERR need at least two snapshots (or specify names)"
		}
		a, b = d.nrSnaps.get(nameA), d.nrSnaps.get(nameB)
	case 1:
		nameA = args[0]
		a = d.nrSnaps.get(nameA)
		if a == nil {
			return "ERR snapshot not found: " + nameA
		}
		nameB = "live"
		var live [256]byte
		for i := 0; i < 256; i++ {
			live[i] = d.emu.nextRegs.Raw(byte(i))
		}
		b = &nrSnapshot{regs: live}
	default:
		nameA, nameB = args[0], args[1]
		a, b = d.nrSnaps.get(nameA), d.nrSnaps.get(nameB)
		if a == nil {
			return "ERR snapshot not found: " + nameA
		}
		if b == nil {
			return "ERR snapshot not found: " + nameB
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "OK %s -> %s\r\n", nameA, nameB)
	diffs := 0
	for r := 0; r < 256; r++ {
		if a.regs[r] == b.regs[r] {
			continue
		}
		fmt.Fprintf(&sb, "  NR$%02X: $%02X -> $%02X\r\n", r, a.regs[r], b.regs[r])
		diffs++
	}
	if diffs == 0 {
		return fmt.Sprintf("OK %s -> %s: no differences", nameA, nameB)
	}
	fmt.Fprintf(&sb, "  (%d differences)", diffs)
	return strings.TrimRight(sb.String(), "\r\n")
}
