package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

// ttEntry is one slot in the time-travel ring.
//
// state is a complete machine state taken through the machinestate registry:
// every device the model carries, checked in both directions when it goes
// back. It replaced a partial capture — CPU registers, the eight visible RAM
// pages, the paging ports and the border — which is what forced reverse
// execution to say "no memory": rewinding to one of those left the sound chip,
// the keyboard, the disk controller and every unmapped bank in the present,
// and on a 128K it paged $7FFD to zero on the way back. Nothing can be
// re-executed forward from a checkpoint like that, which is the whole point of
// keeping one.
type ttEntry struct {
	insn uint64
	// tstates is where the checkpoint sits on the machine's clock. The
	// instruction count alone does not order two instants: it tallies M1
	// fetches, and a halted CPU ticks T-states without retiring one, so
	// two checkpoints either side of a HALT can carry the same count.
	// replay-back needs a real ordering to tell a checkpoint in the past
	// from one the machine has already stepped back past.
	tstates uint64
	pc      uint16
	label   string // optional user-supplied tag (empty for auto-captures)
	state   machinestate.State
	// bankCtx is a compact record of the paging context at capture
	// time (ROM bank, MMU8 slots, divMMC paged/automap/mapram). The
	// state above is what a rewind applies; this is a human-readable
	// echo of it, so `tt-status` traces are immune to bank-aliasing:
	// every captured PC shows which bank it actually executed in, so a
	// $10FB/$14AB-style address can't be mis-attributed to the wrong
	// ROM. Empty on machines whose state is unavailable.
	bankCtx string
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

// timeTravelBuffer is a bounded in-memory ring of complete machine
// states. Auto-capture is triggered from a pre-fetch hook: every
// `everyInsns` Z80 instructions executed, the buffer captures the
// whole machine and evicts the oldest entry when the ring reaches
// `maxEntries`.
//
// Each entry is a machinestate capture — every device the model
// carries, and nothing left in the present. That is what makes the
// ring a replay source rather than only a rewind target: execution
// re-run from one of these produces what it produced the first time,
// which is how replay-back reaches an instruction the ring never
// captured (see replayback.go). It is also the memory cost: an entry
// holds the machine's whole RAM pool, so `tt-on EVERY KEEP` on a Next
// reserves KEEP × ~2 MB.
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

	// replaying suppresses capture while a replay walk re-executes
	// instructions the machine has already run. The pre-fetch hook is
	// still installed during a walk, so without this the ring would
	// fill with checkpoints of a second pass over the same instants and
	// evict the older ones the walk is standing on. Written and read
	// under coreMu — the walk holds it for its whole length, and the
	// hook only ever runs from inside a frame, which also holds it.
	replaying bool
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
	if t.replaying {
		return
	}
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

// capture takes a complete machine state and pushes it into the ring.
// Called from the pre-fetch hook (label="") and from the user-driven
// `tt-snap NAME` command (label=NAME).
//
// A machine with no registered devices — the SAM Coupé, which runs on
// pkg/sam's own core — is skipped rather than stored as an empty
// checkpoint. An empty state restores cleanly and rewinds nothing, so
// keeping one would make tt-rewind and replay-back report success on a
// machine they had not moved.
func (t *timeTravelBuffer) capture(label string, insn uint64) {
	reg := t.emu.stateRegistry()
	if reg.Len() == 0 {
		return
	}
	e := ttEntry{
		insn:    insn,
		tstates: t.emu.cpu.Tstates(),
		pc:      t.emu.cpu.PC,
		label:   label,
		state:   reg.Capture(),
		bankCtx: captureBankContext(t.emu),
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

// Restore returns the machine to the checkpoint, all devices or none.
//
// coreMu is held for the apply because a restore rewrites the CPU
// registers, the whole RAM pool and every device on the bus, all of
// which the emulation goroutine reads every frame. The instruction and
// T-state counters come back with the CPU's own state, so the rewound
// machine is on the checkpoint's timeline without anyone setting the
// counter by hand.
func (t *timeTravelBuffer) Restore(e *ttEntry) error {
	t.emu.coreMu.Lock()
	defer t.emu.coreMu.Unlock()
	return t.restoreLocked(e)
}

// restoreLocked is Restore's body. The caller must hold coreMu — the
// replay walk does, for the whole walk, so the machine cannot be
// stepped by the emulation goroutine part way through one.
func (t *timeTravelBuffer) restoreLocked(e *ttEntry) error {
	return t.emu.stateRegistry().Restore(e.state)
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
