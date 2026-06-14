package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
)

// readWatchSpec is the active memory-READ watch. It fires when guest
// code reads any byte in [lo, hi] of the given 16K RAM bank, halting
// the CPU at the reading instruction. Mirrors memWatchSpec (writes) —
// the read side is what catches a validation routine reading a buffer
// byte and rejecting it, which a write-watch can never see.
type readWatchSpec struct {
	bank int
	lo   uint16
	hi   uint16
}

// readWatchPtr is the live spec, atomic-swapped so the hot read-path
// hook only does one atomic load + a couple of comparisons when no
// watch is active. Reads are far more frequent than writes (every
// opcode fetch is a read), so this stays cheap when idle.
var readWatchPtr atomic.Pointer[readWatchSpec]

// cmdWatchRead implements `watch-read ram BANK FROM TO`. The watch
// fires on any guest read whose bank+offset falls inside the range,
// halting the CPU. Setting a new watch replaces the prior one.
func (d *remoteDebugger) cmdWatchRead(args []string) string {
	if len(args) < 4 || strings.ToLower(args[0]) != "ram" {
		return "ERR usage: watch-read ram BANK FROM TO  (reads from ram bank BANK offset FROM..TO halt the CPU)"
	}
	bank, err := parseHex(args[1])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	lo, err := parseHex(args[2])
	if err != nil {
		return "ERR bad from: " + err.Error()
	}
	hi, err := parseHex(args[3])
	if err != nil {
		return "ERR bad to: " + err.Error()
	}
	if hi < lo {
		return "ERR to must be >= from"
	}
	spec := &readWatchSpec{bank: int(bank), lo: lo, hi: hi}
	readWatchPtr.Store(spec)
	d.ensureReadWatchHook()
	return fmt.Sprintf("OK watching reads from ram bank %d $%04X-$%04X", spec.bank, spec.lo, spec.hi)
}

// cmdClearWatchRead removes the active read watch (if any). The chained
// hook stays installed but no-ops once the spec is nil.
func (d *remoteDebugger) cmdClearWatchRead(_ []string) string {
	readWatchPtr.Store(nil)
	return "OK read watch cleared"
}

// ensureReadWatchHook installs the chained RAM-read hook exactly once.
// It calls any previously-installed read hook first, then evaluates the
// active spec and halts the CPU on a match. Installed only on first
// `watch-read` use so runs that never use it pay no per-read cost.
func (d *remoteDebugger) ensureReadWatchHook() {
	if d.readWatchInstalled {
		return
	}
	d.readWatchInstalled = true
	prior := d.emu.mem.GetRAMReadHook()
	d.emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) {
		if prior != nil {
			prior(bank, addr, val)
		}
		spec := readWatchPtr.Load()
		if spec == nil {
			return
		}
		if bank != spec.bank || addr < spec.lo || addr > spec.hi {
			return
		}
		slog.Info("watch-read hit",
			"bank", bank,
			"addr", fmt.Sprintf("$%04X", addr),
			"val", fmt.Sprintf("$%02X", val),
			"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC))
		d.paused.Store(true)
		d.snapshotOnBPHit(d.emu.cpu.PC, "watch-read")
	})
}
