package main

import (
	"fmt"
	"log/slog"
	"strings"
)

// bpFirstEntrySpec is the active one-shot range trap. When non-nil
// in the atomic pointer, BreakpointCheck halts on the first M1 fetch
// where lo <= PC <= hi and self-clears (sets pointer to nil) so the
// same trap doesn't refire on steady-state visits.
type bpFirstEntrySpec struct {
	lo, hi uint16
	prefix string // snapshot-on-bp prefix; if empty, no snapshot file
}

// cmdBPFirstEntry arms a single-fire breakpoint that halts the CPU on
// the FIRST M1 fetch whose PC lies inside [LO, HI]. Auto-cleared on
// fire — `continue` resumes without re-arming.
//
// Without this, catching "first entry to NOP region $C000-$DFFF" via
// `set-breakpoint` halts on the BP each time the CPU re-enters at
// steady state; you have to wade through many `continue`s to find the
// FIRST occurrence vs subsequent ones. `bp-first-entry` removes that
// distinction by being inherently one-shot.
//
// Combined with `snapshot-on-bp prefix=NAME`-style auto-capture (the
// optional `snap=PREFIX` argument), the fire-and-disarm becomes a
// full state dump so post-hoc analysis can examine exactly the trail
// that led to the first range entry.
//
// Forms:
//
//	bp-first-entry $LO[-$HI] [snap=PREFIX]
//	bp-first-entry off                       (disarm)
//	bp-first-entry                           (show armed range)
//
// LO and HI are hex; HI defaults to LO if omitted (= classic single
// PC trap). PREFIX, if given, names the snapshot file at fire time
// (state goes to <PREFIX>.{json,bin}).
//
// Examples:
//
//	bp-first-entry $C000-$DFFF                # halt on first NOP-slide PC
//	bp-first-entry $C000-$DFFF snap=nop-entry # + auto-snapshot
//	bp-first-entry $0038                      # first IRQ vector fetch
//	bp-first-entry off                        # disarm
func (d *remoteDebugger) cmdBPFirstEntry(args []string) string {
	if len(args) == 0 {
		spec := d.bpFirstEntry.Load()
		if spec == nil {
			return "OK (no bp-first-entry armed)"
		}
		s := *spec
		extra := ""
		if s.prefix != "" {
			extra = fmt.Sprintf(" snap=%s", s.prefix)
		}
		return fmt.Sprintf("OK bp-first-entry $%04X-$%04X%s (armed)", s.lo, s.hi, extra)
	}
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.bpFirstEntry.Store(nil)
		return "OK bp-first-entry cleared"
	}
	// Parse range. Accepts "$LO-$HI" or "$LO" (single PC).
	rangeStr := args[0]
	lo, hi, err := parseFirstEntryRange(rangeStr)
	if err != nil {
		return "ERR " + err.Error()
	}
	prefix := ""
	for _, a := range args[1:] {
		if strings.HasPrefix(strings.ToLower(a), "snap=") {
			prefix = a[len("snap="):]
		} else {
			return "ERR unknown arg: " + a + " (expected snap=PREFIX)"
		}
	}
	spec := &bpFirstEntrySpec{lo: lo, hi: hi, prefix: prefix}
	d.bpFirstEntry.Store(spec)
	// Resume execution so the trap can fire.
	d.paused.Store(false)
	extra := ""
	if prefix != "" {
		extra = fmt.Sprintf(" snap=%s", prefix)
	}
	return fmt.Sprintf("OK bp-first-entry $%04X-$%04X%s; running", lo, hi, extra)
}

// parseFirstEntryRange accepts "$LO-$HI" or "$LO".
func parseFirstEntryRange(s string) (uint16, uint16, error) {
	if dash := strings.Index(s, "-"); dash > 0 {
		lo, err := parseHex(s[:dash])
		if err != nil {
			return 0, 0, fmt.Errorf("bad LO: %w", err)
		}
		hi, err := parseHex(s[dash+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("bad HI: %w", err)
		}
		if lo > hi {
			return 0, 0, fmt.Errorf("LO > HI")
		}
		return lo, hi, nil
	}
	v, err := parseHex(s)
	if err != nil {
		return 0, 0, err
	}
	return v, v, nil
}

// fireBPFirstEntry is called from BreakpointCheck when a hit matches.
// It self-clears, pauses, optionally snapshots, and logs. Public name
// because the hot path is in debugger.go.
func (d *remoteDebugger) fireBPFirstEntry(pc uint16, spec *bpFirstEntrySpec) {
	d.bpFirstEntry.Store(nil)
	d.paused.Store(true)
	slog.Debug("debugger: bp-first-entry hit",
		"pc", fmt.Sprintf("$%04X", pc),
		"range", fmt.Sprintf("$%04X-$%04X", spec.lo, spec.hi))
	reason := "bp-first-entry"
	if spec.prefix != "" {
		reason = "bp-first-entry:" + spec.prefix
	}
	d.snapshotOnBPHit(pc, reason)
}
