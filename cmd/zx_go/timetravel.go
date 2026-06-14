package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// ttEntry is one slot in the time-travel ring. The Snapshot covers
// CPU + visible 8 RAM pages + 7FFD/1FFD ports + border colour. On
// the Next this captures the CPU-visible 64 K accurately but does
// NOT cover the upper RAM banks (8..111), divMMC RAM, NextRegs, or
// MMU8 slot table — adding those is Phase 2 work tracked separately.
type ttEntry struct {
	insn  uint64
	pc    uint16
	label string // optional user-supplied tag (empty for auto-captures)
	snap  *snapshot.Snapshot
	// bankCtx is a compact record of the paging context at capture
	// time (ROM bank, MMU8 slots, divMMC paged/automap/mapram). It is
	// NOT restored by tt-rewind (Phase 1 still rolls back only the
	// lower-64K + regs) — its purpose is to make `tt-status` traces
	// immune to bank-aliasing: every captured PC shows which bank it
	// actually executed in, so a $10FB/$14AB-style address can't be
	// mis-attributed to the wrong ROM. Phase 2a (todo). Empty on
	// classic models / when state is unavailable.
	bankCtx string
	// nextFull is the complete Next machine state (full 2MB pool, MMU8
	// slots, divMMC RAM, NextRegs, paging ports) captured alongside the
	// Phase-1 snapshot. nil on classic models. When present, tt-rewind
	// restores it so forward re-execution from a Next rewind point is
	// faithful — Phase 2b. (Phase 1's snap only covers the lower 64K.)
	nextFull *nextFullState
}

// captureBankContext builds the compact paging-context string stored
// in each ttEntry. Mirrors fmtMMU/fmtDivMMC but is self-contained so
// it can run from the capture hot path with only *emulator.
func captureBankContext(emu *emulator) string {
	if emu == nil || emu.mem == nil {
		return ""
	}
	mem := emu.mem
	port7FFD, port1FFD, _ := mem.GetPortState()
	romBank := int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
	slots := make([]string, 8)
	for i := byte(0); i < 8; i++ {
		slots[i] = fmt.Sprintf("%02X", mem.GetMMU(i))
	}
	ctx := fmt.Sprintf("rom=%d slots=%s", romBank, strings.Join(slots, " "))
	if emu.ula != nil {
		if pager := emu.ula.NextDivMMC(); pager != nil {
			type isPagedIn interface{ IsPagedIn() bool }
			type isMAPRAM interface{ MAPRAM() bool }
			type isAutomap interface{ AutomapEnabled() bool }
			paged, mapram, automap := false, false, false
			if x, ok := pager.(isPagedIn); ok {
				paged = x.IsPagedIn()
			}
			if x, ok := pager.(isMAPRAM); ok {
				mapram = x.MAPRAM()
			}
			if x, ok := pager.(isAutomap); ok {
				automap = x.AutomapEnabled()
			}
			ctx += fmt.Sprintf(" dmmc:paged=%v automap=%v mapram=%v", paged, automap, mapram)
		}
	}
	return ctx
}

// timeTravelBuffer is a bounded in-memory ring of periodic emulator
// snapshots. Auto-capture is triggered from a pre-fetch hook: every
// `everyInsns` Z80 instructions executed, the buffer takes a new
// full-state snapshot and evicts the oldest entry when the ring
// reaches `maxEntries`.
//
// Captures store the FULL CPU register set plus the 8 visible RAM
// pages — enough to rewind boot-time state on classic models and
// the lower-64 K window on the Next. The MMU8 slot table, upper
// banks, and NextRegs are NOT yet captured; this is a known Phase 1
// limitation called out in the documentation.
// ttController adapts the emulator-owned time-travel buffer to the
// debugger.TimeTravelController interface the GUI Time-Travel tab
// drives. It creates/destroys the buffer (and its CPU pre-fetch
// hook) on Enable/Disable, so the GUI and the telnet tt-* commands
// operate on the SAME emu.timeTravel ring.
type ttController struct{ emu *emulator }

func (c ttController) Enabled() bool { return c.emu.timeTravel != nil }

func (c ttController) Enable(everyInsns, keep int) {
	if c.emu.timeTravel != nil {
		c.emu.cpu.RemovePreFetchHook("debug-time-travel")
		c.emu.timeTravel = nil
	}
	tt := newTimeTravelBuffer(c.emu, everyInsns, keep)
	if tt == nil {
		return
	}
	c.emu.timeTravel = tt
	c.emu.cpu.AddPreFetchHook("debug-time-travel", tt.Step)
}

func (c ttController) Disable() {
	if c.emu.timeTravel == nil {
		return
	}
	c.emu.cpu.RemovePreFetchHook("debug-time-travel")
	c.emu.timeTravel = nil
}

func (c ttController) Snap(label string) {
	if c.emu.timeTravel != nil {
		c.emu.timeTravel.capture(label, c.emu.cpu.InstructionCount())
	}
}

func (c ttController) Rewind(insn uint64) error {
	if c.emu.timeTravel == nil {
		return fmt.Errorf("time-travel off")
	}
	e := c.emu.timeTravel.FindAtOrBefore(insn)
	if e == nil {
		return fmt.Errorf("no snapshot ≤ %d", insn)
	}
	return c.emu.timeTravel.Restore(e)
}

func (c ttController) Clear() {
	if c.emu.timeTravel != nil {
		c.emu.timeTravel.Clear()
	}
}

func (c ttController) Rows() []debugger.TTRow {
	if c.emu.timeTravel == nil {
		return nil
	}
	snaps := c.emu.timeTravel.Snapshots()
	out := make([]debugger.TTRow, 0, len(snaps))
	for _, e := range snaps {
		out = append(out, debugger.TTRow{Insn: e.insn, PC: e.pc, Label: e.label})
	}
	return out
}

type timeTravelBuffer struct {
	mu sync.Mutex

	everyInsns int
	maxEntries int
	entries    []ttEntry

	emu *emulator

	// nextCapture is the instruction-count threshold at which the
	// next auto-capture should fire. Read in the pre-fetch hot path
	// without holding the mutex (single-writer = the hook), so the
	// load order is hook → check → mutate-under-mutex.
	nextCapture uint64

	// lastSeenInsns: detects an instruction-counter rewind (cold-
	// reset or RZX playback start). When seen, nextCapture re-arms
	// at the post-rewind baseline so captures resume promptly rather
	// than skipping the first everyInsns × maxEntries insns of
	// post-reset execution.
	lastSeenInsns uint64
}

// newTimeTravelBuffer constructs the ring. Returns nil if
// everyInsns/maxEntries are not sensible.
func newTimeTravelBuffer(emu *emulator, everyInsns, maxEntries int) *timeTravelBuffer {
	if everyInsns <= 0 || maxEntries <= 0 {
		return nil
	}
	return &timeTravelBuffer{
		everyInsns: everyInsns,
		maxEntries: maxEntries,
		emu:        emu,
		// First auto-capture lands at insn = everyInsns. Lets a
		// caller take a manual capture at insn=0 for the boot
		// baseline without colliding with the first auto.
		nextCapture: uint64(everyInsns),
	}
}

// Step is the pre-fetch hook callback. Cheap fast-path: a single
// uint64 read + compare. Capture is rare.
func (t *timeTravelBuffer) Step(_ uint16) {
	insns := t.emu.cpu.InstructionCount()
	if insns < t.lastSeenInsns {
		// Counter rewound — cold-reset or RZX-playback start.
		// Re-arm threshold at the new baseline so we don't skip
		// the early post-reset window.
		t.nextCapture = insns + uint64(t.everyInsns)
	}
	t.lastSeenInsns = insns
	if insns < t.nextCapture {
		return
	}
	t.capture("", insns)
	t.nextCapture = insns + uint64(t.everyInsns)
}

// capture takes a snapshot of current emulator state and pushes it
// into the ring. Called from the pre-fetch hook (label="") and
// from the user-driven `tt-snap NAME` command (label=NAME).
func (t *timeTravelBuffer) capture(label string, insn uint64) {
	snap, err := createSnapshotFromEmulator(t.emu)
	if err != nil {
		return
	}
	e := ttEntry{
		insn:     insn,
		pc:       t.emu.cpu.PC,
		label:    label,
		snap:     snap,
		bankCtx:  captureBankContext(t.emu),
		nextFull: captureNextStateFromEmu(t.emu),
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) >= t.maxEntries {
		// Drop the oldest. copy is fine for the modest ring sizes
		// we expect (≤64 typical); the snapshots themselves are
		// the storage cost, not the slice header shuffling.
		t.entries = append(t.entries[:0], t.entries[1:]...)
	}
	t.entries = append(t.entries, e)
}

// Snapshots returns a copy of the current ring (oldest first) so
// callers can inspect without locking.
func (t *timeTravelBuffer) Snapshots() []ttEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ttEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// FindAtOrBefore returns the latest entry whose insn ≤ target, or
// nil if none. Used by tt-rewind to pick the snapshot to restore.
func (t *timeTravelBuffer) FindAtOrBefore(target uint64) *ttEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *ttEntry
	for i := range t.entries {
		if t.entries[i].insn <= target {
			best = &t.entries[i]
		} else {
			break
		}
	}
	if best == nil {
		return nil
	}
	// Return a stable copy so the caller doesn't hold a pointer
	// into the live slice (eviction would invalidate it).
	cp := *best
	return &cp
}

// FindPC returns every entry whose saved PC equals pc. Useful for
// "show me every snapshot whose PC was inside the freeze loop".
func (t *timeTravelBuffer) FindPC(pc uint16) []ttEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []ttEntry
	for _, e := range t.entries {
		if e.pc == pc {
			out = append(out, e)
		}
	}
	return out
}

// Restore applies the given snapshot back to the emulator. Pause
// management lives inside applySnapshotToEmulator. On the Next, the
// full machine state (whole pool + MMU8 + divMMC RAM + NextRegs +
// paging) is restored after the Phase-1 lower-64K snapshot so that
// forward re-execution from the rewind point is faithful (Phase 2b).
func (t *timeTravelBuffer) Restore(e *ttEntry) error {
	if err := applySnapshotToEmulator(t.emu, e.snap); err != nil {
		return err
	}
	restoreNextStateToEmu(t.emu, e.nextFull)
	// Rewind the instruction counter to the snapshot's timeline so
	// insn-gated tools (tt-find-pc, trace windows, diagnostics) see
	// a consistent history after the rewind.
	t.emu.cpu.SetInstructionCount(e.insn)
	return nil
}

// Clear empties the ring (frees the snapshot memory). Used by
// `tt-clear` from the debugger and by the test harness.
func (t *timeTravelBuffer) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = nil
}

// Len reports the current ring depth. Race-tolerant — value may
// shift before the caller acts on it.
func (t *timeTravelBuffer) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}
