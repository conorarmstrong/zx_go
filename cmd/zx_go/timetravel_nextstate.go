package main

import (
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// divmmcRAMSnapshotter is the subset of *divmmc.Pager the full-state
// capture needs. Using an interface keeps the time-travel layer
// decoupled from the concrete pager and makes capture/restore
// unit-testable without a full emulator.
type divmmcRAMSnapshotter interface {
	SnapshotRAM() [][]byte
	RestoreRAM([][]byte)
}

// nextFullState is the complete Next machine state beyond the Phase-1
// CPU + lower-64K snapshot: the whole 2 MB pool, the MMU8 slot table,
// the divMMC RAM, the NextReg file, and the paging ports. Captured per
// time-travel snapshot (Phase 2b) so tt-rewind restores faithful Next
// state and forward re-execution from a rewind point is valid — the
// Phase-1 ring only rolls back the lower 64 K + registers.
type nextFullState struct {
	pool      [][]byte
	mmu       [8]byte
	divmmcRAM [][]byte
	nextRegs  [256]byte
	paging    *memory.PagingState
}

// captureNextFullState snapshots the full Next state from its
// components. nil mem returns nil; nil nr/pager are simply skipped
// (e.g. classic models have no divMMC RAM).
func captureNextFullState(mem *memory.Memory, nr *nextregs.Dispatcher, pager divmmcRAMSnapshotter) *nextFullState {
	if mem == nil {
		return nil
	}
	fs := &nextFullState{
		pool:   mem.SnapshotPool(),
		mmu:    mem.SnapshotMMU(),
		paging: mem.SavePagingState(),
	}
	if nr != nil {
		for r := 0; r < 256; r++ {
			fs.nextRegs[r] = nr.Raw(byte(r))
		}
	}
	if pager != nil {
		fs.divmmcRAM = pager.SnapshotRAM()
	}
	return fs
}

// restoreNextFullState writes a captured full Next state back. NextRegs
// are restored via Store (raw, no OnWrite side-effects) because the
// derived paging state is restored explicitly afterwards; MMU8 + paging
// are applied last so the read/write page maps end up consistent.
func restoreNextFullState(mem *memory.Memory, nr *nextregs.Dispatcher, pager divmmcRAMSnapshotter, fs *nextFullState) {
	if mem == nil || fs == nil {
		return
	}
	mem.RestorePool(fs.pool)
	if nr != nil {
		for r := 0; r < 256; r++ {
			nr.Store(byte(r), fs.nextRegs[r])
		}
	}
	if pager != nil && fs.divmmcRAM != nil {
		pager.RestoreRAM(fs.divmmcRAM)
	}
	mem.RestoreMMU(fs.mmu)
	mem.RestorePagingState(fs.paging)
}

// emuDivmmcSnapshotter returns the emulator's divMMC pager as a
// divmmcRAMSnapshotter, or nil if there is no Next divMMC pager wired
// (classic models, or a pager that doesn't implement the interface).
func emuDivmmcSnapshotter(emu *emulator) divmmcRAMSnapshotter {
	if emu == nil || emu.ula == nil {
		return nil
	}
	if p, ok := emu.ula.NextDivMMC().(divmmcRAMSnapshotter); ok {
		return p
	}
	return nil
}

// captureNextStateFromEmu captures full Next state from a live
// emulator. Returns nil for non-Next models (the Phase-1 ring already
// covers classic machines fully) and for a nil/RAM-less emulator.
func captureNextStateFromEmu(emu *emulator) *nextFullState {
	if emu == nil || emu.mem == nil || emu.mem.GetCurrentModel() != roms.ModelNext {
		return nil
	}
	return captureNextFullState(emu.mem, emu.nextRegs, emuDivmmcSnapshotter(emu))
}

// restoreNextStateToEmu applies a captured full Next state back to a
// live emulator. Nil-safe (nil emu or fs is a no-op).
func restoreNextStateToEmu(emu *emulator, fs *nextFullState) {
	if emu == nil || fs == nil {
		return
	}
	restoreNextFullState(emu.mem, emu.nextRegs, emuDivmmcSnapshotter(emu), fs)
}
