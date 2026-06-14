package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
)

// memWatchSpec is the active memory-write watch. ram-kind watches
// fire when any guest write lands in [Lo, Hi] of the given Bank.
// One watch may be active at a time; setting a new one replaces
// it. Clear by passing nil to the atomic.Pointer.
type memWatchSpec struct {
	bank int
	lo   uint16
	hi   uint16
}

// memWatchPtr is the live spec, atomic-swapped so the hot-path
// hook installed on Memory only does one atomic load + a couple
// of comparisons when no watch is active.
var memWatchPtr atomic.Pointer[memWatchSpec]

// cmdWatchMem implements `watch-mem ram BANK FROM TO`. The watch
// fires on any guest write whose bank+offset falls inside the
// range, halting the CPU. Setting a new watch replaces the prior
// one; only one is active at a time.
func (d *remoteDebugger) cmdWatchMem(args []string) string {
	if len(args) < 4 || strings.ToLower(args[0]) != "ram" {
		return "ERR usage: watch-mem ram BANK FROM TO  (writes into ram bank BANK offset FROM..TO halt the CPU)"
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
	spec := &memWatchSpec{bank: int(bank), lo: lo, hi: hi}
	memWatchPtr.Store(spec)
	d.ensureMemWatchHook()
	return fmt.Sprintf("OK watching writes to ram bank %d $%04X-$%04X", spec.bank, spec.lo, spec.hi)
}

// cmdClearWatchMem removes the active memory-write watch (if any).
// The chained hook stays installed but no-ops once the spec is nil.
func (d *remoteDebugger) cmdClearWatchMem(_ []string) string {
	memWatchPtr.Store(nil)
	return "OK memory watch cleared"
}

// ensureMemWatchHook installs the chained hook exactly once. The
// chain calls the previously-installed hook (if any) — typically
// the env-var diagnostic tracer — then evaluates the active watch
// spec and halts the CPU if a match is found.
func (d *remoteDebugger) ensureMemWatchHook() {
	if d.memWatchInstalled {
		return
	}
	d.memWatchInstalled = true
	prior := d.emu.mem.GetRAMWriteHook()
	d.emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
		if prior != nil {
			prior(bank, addr, val)
		}
		spec := memWatchPtr.Load()
		if spec == nil {
			return
		}
		if bank != spec.bank || addr < spec.lo || addr > spec.hi {
			return
		}
		slog.Info("watch-mem hit",
			"bank", bank,
			"addr", fmt.Sprintf("$%04X", addr),
			"val", fmt.Sprintf("$%02X", val),
			"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC))
		d.paused.Store(true)
		d.snapshotOnBPHit(d.emu.cpu.PC, "watch-mem")
	})
}
