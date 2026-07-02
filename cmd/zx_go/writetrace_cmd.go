package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
)

// traceWritesSpec is the active filter for trace-writes. Stored
// behind atomic.Pointer so the hot-path WriteObserver doesn't lock.
type traceWritesSpec struct {
	lo, hi uint16 // inclusive virtual-address range
	limit  uint64 // max log lines (0 = unlimited)
}

// traceWritesState is per-debugger state for trace-writes.
type traceWritesState struct {
	spec      atomic.Pointer[traceWritesSpec]
	count     atomic.Uint64
	installed bool
	// prior is the previously-installed WriteObserver (if any) — we
	// chain on top so env-var diagnostics keep working.
	prior func(addr uint16, val byte, pc uint16)
}

// cmdTraceWrites arms a CPU-write tracer that logs every store whose
// virtual destination falls inside [LO, HI], resolving the actual
// destination bank + offset + MMU slot at the moment of the write.
//
// Why this exists: the existing `ramWriteHook` only reports the
// resolved (bank, offset) for writes that go through Memory.Write's
// standard RAM path. Config-mode writes (NR$04 RAMPAGE), alt-rom
// redirects, FPGA-bootrom INIR loops, and divMMC overlay writes all
// land via DIFFERENT dispatch paths — the existing tooling lumps them
// together as "bank N writes" without distinguishing config-mode
// routing from MMU8 mapping. `trace-writes` exposes the full routing
// context so the two are never conflated.
//
// Each log line is one slog.Info("write-trace") event with fields:
//
//	pc          — CPU PC of the writing instruction (M1 fetch)
//	virt        — virtual address the CPU wrote to ($0000-$FFFF)
//	val         — byte value
//	slot8k      — 8K slot containing virt (= virt >> 13)
//	bank8k      — MMU8 bank value at that slot ($00-$DF, or $FF=ROM)
//	mmu_override — whether MMU8 has been written for that slot
//	rom_bank    — current ROM bank selection
//	insns       — instruction count
//
// Forms:
//
//	trace-writes                         show armed spec + hit count
//	trace-writes off                     disarm
//	trace-writes $LO-$HI [limit=N]       arm for range
//	trace-writes $LO [limit=N]           single-address arm
//
// Examples:
//
//	trace-writes $C000-$DFFF                # all writes to slot 6
//	trace-writes $5B8A limit=20             # CALLERS_RET sysvar writes
//	trace-writes $0000-$3FFF limit=500      # FPGA-bootrom INIR phase
func (d *remoteDebugger) cmdTraceWrites(args []string) string {
	if len(args) == 0 {
		spec := d.traceWrites.spec.Load()
		if spec == nil {
			return "OK trace-writes off"
		}
		s := *spec
		extra := ""
		if s.limit > 0 {
			extra = fmt.Sprintf(" limit=%d", s.limit)
		}
		return fmt.Sprintf("OK trace-writes $%04X-$%04X%s hits=%d",
			s.lo, s.hi, extra, d.traceWrites.count.Load())
	}
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.traceWrites.spec.Store(nil)
		d.traceWrites.count.Store(0)
		return "OK trace-writes cleared"
	}
	lo, hi, err := parseFirstEntryRange(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	spec := &traceWritesSpec{lo: lo, hi: hi}
	for _, a := range args[1:] {
		if strings.HasPrefix(strings.ToLower(a), "limit=") {
			v, err := parseHex(a[len("limit="):])
			if err != nil {
				// Try decimal.
				return "ERR bad limit: " + err.Error()
			}
			spec.limit = uint64(v)
		} else {
			return "ERR unknown arg: " + a + " (expected limit=N)"
		}
	}
	d.traceWrites.spec.Store(spec)
	d.traceWrites.count.Store(0)
	d.ensureTraceWritesHook()
	extra := ""
	if spec.limit > 0 {
		extra = fmt.Sprintf(" limit=%d", spec.limit)
	}
	return fmt.Sprintf("OK trace-writes $%04X-$%04X%s", spec.lo, spec.hi, extra)
}

// ensureTraceWritesHook plants the chained WriteObserver exactly
// once. The chain calls the previously-installed observer first
// (typically nil), then evaluates our spec.
func (d *remoteDebugger) ensureTraceWritesHook() {
	if d.traceWrites.installed {
		return
	}
	d.traceWrites.installed = true
	d.traceWrites.prior = d.emu.mem.WriteObserver
	d.emu.mem.WriteObserver = func(addr uint16, val byte, pc uint16) {
		if d.traceWrites.prior != nil {
			d.traceWrites.prior(addr, val, pc)
		}
		spec := d.traceWrites.spec.Load()
		if spec == nil {
			return
		}
		if addr < spec.lo || addr > spec.hi {
			return
		}
		c := d.traceWrites.count.Add(1)
		if spec.limit > 0 && c > spec.limit {
			return
		}
		cpu := d.emu.cpu
		// Resolve destination: which 8K slot, which bank at that slot.
		slot := byte(addr >> 13)
		bank8k := d.emu.mem.GetMMU(slot)
		ovr := d.emu.mem.IsMMUOverridden(slot)
		slog.Info("write-trace",
			"pc", fmt.Sprintf("$%04X", cpu.PC),
			"virt", fmt.Sprintf("$%04X", addr),
			"val", fmt.Sprintf("$%02X", val),
			"slot8k", slot,
			"bank8k", fmt.Sprintf("$%02X", bank8k),
			"mmu_override", ovr,
			"rom_bank", d.currentROMBank(),
			"insns", cpu.InstructionCount())
	}
}
