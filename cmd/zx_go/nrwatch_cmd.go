package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// `watch-nextreg` halts on a write to a named NextReg.
//
// It exists because neither half of the pair could do it alone. `nr-trace`
// names registers and logs their writes without stopping. `watch-port` stops,
// but a NextReg write is a select on $243B followed by a value on $253B, so a
// watch on either port sees half a transaction: $243B carries the register
// with no value, $253B the value with no register, and a value match on $253B
// matches the value whatever register it lands in.
//
// The dispatcher's tracer sees the pair already joined: it is called with
// (reg, val) after the write has been applied, so keying a watch on it is
// exact for both the two-port sequence and the single-write $57 path.
//
// Copper MOVEs trip these watches too, and that is deliberate: a MOVE goes
// through the same Dispatcher.WriteReg the CPU reaches through the ports, and
// "which copper list is stamping on this register" is one of the questions the
// command exists to answer. The hit line names the writer so a copper MOVE is
// never reported against whatever address the CPU happened to be at.

// nrWatchSpec is one armed NextReg-write watch. matchVal == nil means "any
// value"; otherwise the watch fires only when the byte written equals
// *matchVal. logOnly emits the hit line without halting, which is what you
// want for a register written every frame.
type nrWatchSpec struct {
	reg      byte
	matchVal *byte
	logOnly  bool
}

// nrWatchSet protects the live watch list. A telnet command mutates it under
// the mutex; the dispatcher tracer reads it on every NextReg write.
type nrWatchSet struct {
	mu      sync.Mutex
	watches []nrWatchSpec
}

func (s *nrWatchSet) add(spec nrWatchSpec) {
	s.mu.Lock()
	s.watches = append(s.watches, spec)
	s.mu.Unlock()
}

func (s *nrWatchSet) clear() {
	s.mu.Lock()
	s.watches = nil
	s.mu.Unlock()
}

// match reports whether a write to reg with value val satisfies any armed
// watch, and whether any of the satisfied ones halts. A log-only watch and a
// halting watch on the same register both report; halt wins.
func (s *nrWatchSet) match(reg, val byte) (matched, halt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.watches {
		if w.reg != reg {
			continue
		}
		if w.matchVal != nil && *w.matchVal != val {
			continue
		}
		matched = true
		if !w.logOnly {
			halt = true
			return
		}
	}
	return
}

func (s *nrWatchSet) list() []nrWatchSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]nrWatchSpec, len(s.watches))
	copy(out, s.watches)
	return out
}

// cmdWatchNextReg arms a NextReg-write watch.
//
//	watch-nextreg                  list the armed watches
//	watch-nextreg REG[,REG…]       halt on any write to those registers
//	watch-nextreg REG =VAL         halt only when the byte written equals VAL
//	watch-nextreg REG[,REG…] log   log every write, do NOT halt
//	watch-nextreg off              clear all
//
// Registers are hex, with or without a leading $, matching nr-trace.
func (d *remoteDebugger) cmdWatchNextReg(args []string) string {
	if len(args) >= 1 && strings.ToLower(args[0]) == "off" {
		d.nrWatches.clear()
		return "OK nextreg watches cleared"
	}
	// "log" is a mode, never a register, so it is stripped wherever it is the
	// last argument. Guarding on there being another argument left made a bare
	// `watch-nextreg log` fall through and fail as a bad hex register.
	logOnly := false
	if len(args) >= 1 && strings.ToLower(args[len(args)-1]) == "log" {
		logOnly = true
		args = args[:len(args)-1]
	}
	// `watch-nextreg log` asked to arm something and named nothing, which is a
	// mistake to report rather than a request to list.
	if logOnly && len(args) == 0 {
		return "ERR watch-nextreg: no register given"
	}
	if len(args) < 1 {
		ws := d.nrWatches.list()
		if len(ws) == 0 {
			return "OK (no nextreg watches)"
		}
		var b strings.Builder
		b.WriteString("OK\r\n")
		for _, w := range ws {
			mode := "halt"
			if w.logOnly {
				mode = "log-only"
			}
			if w.matchVal != nil {
				fmt.Fprintf(&b, "  NR$%02X val=$%02X (%s)\r\n", w.reg, *w.matchVal, mode)
			} else {
				fmt.Fprintf(&b, "  NR$%02X (any value, %s)\r\n", w.reg, mode)
			}
		}
		return strings.TrimRight(b.String(), "\r\n")
	}

	// Arming a watch needs the NextReg dispatcher to hang the hook on, and only
	// the Next has one: cmd/zx_go/next.go nils nextRegs for every other model.
	// Refuse rather than dereference it. Clearing and listing above are pure
	// bookkeeping and stay usable on any machine.
	if d.emu.nextRegs == nil {
		return "ERR watch-nextreg: this machine has no NextRegs (Next-only command)"
	}

	// Parse REG[,REG…] [=VAL]. "=" may be glued onto either side. A value
	// match applies to every register in the list, which is what "watch
	// these three for the moment any of them goes to $00" wants.
	raw := strings.TrimSpace(strings.Join(args, " "))
	regStr, valStr := raw, ""
	if i := strings.Index(raw, "="); i >= 0 {
		regStr = strings.TrimSpace(raw[:i])
		valStr = strings.TrimSpace(raw[i+1:])
	}
	var matchVal *byte
	if valStr != "" {
		v, err := parseHex(valStr)
		if err != nil {
			return "ERR bad val: " + err.Error()
		}
		b := byte(v & 0xFF)
		matchVal = &b
	}

	// Parse the whole list before arming any of it. Adding as we went left a
	// list that failed halfway partly armed while the command reported an
	// error and returned before installing the hook, so the watches nobody was
	// told about fired the next time some other command installed it.
	var regs []byte
	for _, p := range strings.Split(strings.ReplaceAll(regStr, " ", ","), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseHex(p)
		if err != nil {
			return "ERR bad reg " + p + ": " + err.Error()
		}
		regs = append(regs, byte(v&0xFF))
	}
	if len(regs) == 0 {
		return "ERR watch-nextreg: no register given"
	}
	added := make([]string, 0, len(regs))
	for _, reg := range regs {
		d.nrWatches.add(nrWatchSpec{reg: reg, matchVal: matchVal, logOnly: logOnly})
		added = append(added, fmt.Sprintf("$%02X", reg))
	}
	d.ensureNRWatchHook()

	mode := "halt"
	if logOnly {
		mode = "log-only"
	}
	if matchVal != nil {
		return fmt.Sprintf("OK watching NR%s for val=$%02X (%s)",
			strings.Join(added, ","), *matchVal, mode)
	}
	return fmt.Sprintf("OK watching NR%s (any value, %s)", strings.Join(added, ","), mode)
}

// ensureNRWatchHook chains a tracer onto the NextReg dispatcher on the first
// watch-nextreg invocation. Any previously-installed tracer fires first, so
// arming a watch never silences an nr-trace or the startup env-var tracer.
func (d *remoteDebugger) ensureNRWatchHook() {
	if d.nrWatchHookedOn == d.emu.nextRegs {
		return
	}
	d.nrWatchHookedOn = d.emu.nextRegs
	prior := d.emu.nextRegs.GetTracer()
	d.emu.nextRegs.SetTracer(func(reg, val byte, isWrite bool) {
		if prior != nil {
			prior(reg, val, isWrite)
		}
		if !isWrite {
			return
		}
		matched, halt := d.nrWatches.match(reg, val)
		if !matched {
			return
		}
		by, where := d.nextRegWriter()
		slog.Info("watch-nextreg hit",
			"reg", fmt.Sprintf("$%02X", reg),
			"val", fmt.Sprintf("$%02X", val),
			"by", by,
			"at", where)
		if halt {
			d.paused.Store(true)
			d.snapshotOnBPHit(d.emu.cpu.PC, "watch-nextreg")
		}
	})
}

// nextRegWriter names whoever performed the write now being traced, and where
// it came from. A copper MOVE reaches the same dispatcher the CPU does, so
// reporting the Z80's PC for one would name an instruction that had nothing to
// do with the write. The copper raises a flag across the write itself, which
// is the only instant this is called at.
func (d *remoteDebugger) nextRegWriter() (by, where string) {
	if c := d.emu.nextCopper; c != nil && c.Executing() {
		return "copper", fmt.Sprintf("copper instruction %d", c.PC())
	}
	return "cpu", fmt.Sprintf("$%04X", d.emu.cpu.PC)
}
