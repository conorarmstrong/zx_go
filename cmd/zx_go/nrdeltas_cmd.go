package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// nrDeltaSet is the runtime set of NextReg numbers to log transitions
// for. Distinct from `nr-trace` (which logs every write); deltas log
// only writes that actually change the stored value.
type nrDeltaSet struct {
	mu      sync.Mutex
	watched map[byte]bool
	all     bool
}

func (s *nrDeltaSet) addAll() {
	s.mu.Lock()
	s.all = true
	s.mu.Unlock()
}

func (s *nrDeltaSet) add(reg byte) {
	s.mu.Lock()
	if s.watched == nil {
		s.watched = map[byte]bool{}
	}
	s.watched[reg] = true
	s.mu.Unlock()
}

func (s *nrDeltaSet) clear() {
	s.mu.Lock()
	s.watched = nil
	s.all = false
	s.mu.Unlock()
}

func (s *nrDeltaSet) match(reg byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.all {
		return true
	}
	return s.watched[reg]
}

// isAll reports whether the set is in "watch every register" mode.
// Kept distinct from list() (rather than encoded as a sentinel in its
// return value) because $FF is itself a valid, if reserved, NextReg
// number and so cannot double as an "all" marker without colliding
// with a set that watches only $FF.
func (s *nrDeltaSet) isAll() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all
}

func (s *nrDeltaSet) list() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, 0, len(s.watched))
	for r := range s.watched {
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// nrDeltaState is per-debugger state for trace-nextreg-deltas. The
// prev array tracks last-seen value per reg ($00-$FF) so the tracer
// can emit only on actual transitions.
type nrDeltaState struct {
	set       nrDeltaSet
	count     atomic.Uint64
	installed bool
	// prev is the last value we saw for each reg, indexed by reg
	// number. Stored under set.mu to avoid races with reset.
	prev [256]byte
	// prevSeen tracks whether prev[reg] reflects an observed value
	// (true) or is still the initial zero placeholder. Without this
	// every first observation of a non-zero reg would log a spurious
	// "0 → N" transition. Bit-packed to keep memory tiny.
	prevSeen [256]bool
}

// cmdTraceNRDeltas arms a NextReg-transition tracer that logs ONLY
// writes that actually change the stored value (i.e. value-deltas).
// Distinct from `nr-trace` which logs every write — for hot NextRegs
// like NR$04 (RAMPAGE) that take tens of thousands of writes per
// boot, the delta filter cuts the log volume by >1000x while still
// capturing every meaningful transition.
//
// Each emitted line is one slog.Info("nr-delta") event with fields:
//
//	reg    — NextReg number
//	old    — value before this write
//	new    — value after this write
//	pc     — PC of the writing instruction
//	insns  — instruction count at the transition
//
// Forms:
//
//	trace-nextreg-deltas                    show armed set + hit count
//	trace-nextreg-deltas off                disarm
//	trace-nextreg-deltas all                log every NR's transitions
//	trace-nextreg-deltas REG[,REG…]         log only listed NRs (hex)
//
// Examples:
//
//	trace-nextreg-deltas 56,57              # MMU8 slot 6/7 transitions
//	trace-nextreg-deltas 8E,8C,04           # paging + alt-rom + RAMPAGE
//	trace-nextreg-deltas all                # full NextReg transition log
func (d *remoteDebugger) cmdTraceNRDeltas(args []string) string {
	if len(args) == 0 {
		if d.nrDeltas.set.isAll() {
			return fmt.Sprintf("OK trace-nextreg-deltas all hits=%d",
				d.nrDeltas.count.Load())
		}
		regs := d.nrDeltas.set.list()
		if len(regs) == 0 {
			return "OK trace-nextreg-deltas off"
		}
		parts := make([]string, len(regs))
		for i, r := range regs {
			parts[i] = fmt.Sprintf("$%02X", r)
		}
		return fmt.Sprintf("OK trace-nextreg-deltas=%s hits=%d",
			strings.Join(parts, ","), d.nrDeltas.count.Load())
	}
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.nrDeltas.set.clear()
		d.nrDeltas.count.Store(0)
		// Reset prevSeen so the next arm starts fresh.
		d.nrDeltas.set.mu.Lock()
		d.nrDeltas.prevSeen = [256]bool{}
		d.nrDeltas.set.mu.Unlock()
		return "OK trace-nextreg-deltas cleared"
	}
	if strings.EqualFold(args[0], "all") {
		d.nrDeltas.set.addAll()
		d.ensureNRDeltasHook()
		return "OK trace-nextreg-deltas all"
	}
	for _, p := range strings.Split(strings.Join(args, ","), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseHex(p)
		if err != nil {
			return "ERR bad reg " + p + ": " + err.Error()
		}
		d.nrDeltas.set.add(byte(v & 0xFF))
	}
	d.ensureNRDeltasHook()
	return "OK trace-nextreg-deltas armed"
}

// ensureNRDeltasHook plants the chained tracer once. Cooperates with
// the existing nr-trace tracer — both can be armed at the same time.
func (d *remoteDebugger) ensureNRDeltasHook() {
	if d.nrDeltas.installed {
		return
	}
	d.nrDeltas.installed = true
	prior := d.emu.nextRegs.GetTracer()
	d.emu.nextRegs.SetTracer(func(reg, val byte, isWrite bool) {
		if prior != nil {
			prior(reg, val, isWrite)
		}
		if !isWrite {
			return
		}
		if !d.nrDeltas.set.match(reg) {
			return
		}
		d.nrDeltas.set.mu.Lock()
		oldVal := d.nrDeltas.prev[reg]
		seen := d.nrDeltas.prevSeen[reg]
		d.nrDeltas.prev[reg] = val
		d.nrDeltas.prevSeen[reg] = true
		d.nrDeltas.set.mu.Unlock()
		// First observation logs as "init" instead of a delta to
		// avoid the spurious "0 → val" line every fresh reg picks up.
		if !seen {
			d.nrDeltas.count.Add(1)
			slog.Info("nr-delta",
				"reg", fmt.Sprintf("$%02X", reg),
				"old", "init",
				"new", fmt.Sprintf("$%02X", val),
				"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC),
				"insns", d.emu.cpu.InstructionCount())
			return
		}
		if oldVal == val {
			return // not a delta — silently drop
		}
		d.nrDeltas.count.Add(1)
		slog.Info("nr-delta",
			"reg", fmt.Sprintf("$%02X", reg),
			"old", fmt.Sprintf("$%02X", oldVal),
			"new", fmt.Sprintf("$%02X", val),
			"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC),
			"insns", d.emu.cpu.InstructionCount())
	})
}
