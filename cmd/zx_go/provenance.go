package main

import "sync/atomic"

// provenance.go implements the data-lineage core of the backward
// provenance tracer (Tool #1). It answers "which instruction last
// wrote this byte?" — the missing causal dimension that the
// time-travel ring (which only answers "what was the state at time
// T?") cannot provide on its own.
//
// A common boot-failure shape in this emulator is a bad control
// transfer / corrupt stack that lands PC somewhere fatal (e.g. a
// bank-2 $0000 NOP;JR trap). Reconstructing the cause by hand —
// rewind, single-step, eyeball every RET/JP, guess — is slow.
// Provenance turns that into a direct query: at the trap, read the
// return word off the stack and ask who pushed it, then who computed
// that value.
//
// Design: a flat last-writer table indexed by the 16-bit LOGICAL
// address (what the CPU's SP/stack operate on). Each successful CPU
// write stamps the cell with the writing instruction's counter, PC,
// the value, and the ROM-bank context so cross-bank transfers are
// visible. Last-writer-wins — older writers to the same cell are
// overwritten, which is exactly what we want (the most recent push to
// a stack slot is the one a RET will pop).
//
// Caveat: keyed by logical address, so a later paging change that
// remaps the same logical address to a different physical bank can
// make a stale record misleading. For stack archaeology (the target
// use) the stack lives in a stable RAM region across the few
// instructions between push and pop, so this is not a problem in
// practice. The romBank field lets a query spot the rare case.

// provRec is the last-writer record for one logical address.
type provRec struct {
	valid   bool
	insn    uint64 // CPU instruction counter at the time of the write
	pc      uint16 // PC of the writing instruction
	val     byte   // the byte value written
	romBank int    // ROM-bank context at write time (-1 if unknown)
}

// provenanceTracker holds the last-writer table. It is armed lazily
// (enabled) so the hot-path write hook is a single atomic load + a
// no-op when provenance is not in use.
type provenanceTracker struct {
	enabled atomic.Bool
	mem     [1 << 16]provRec

	// installed guards one-time chaining of the WriteObserver; prior
	// is the observer we chain on top of so other diagnostics keep
	// working.
	installed bool
	prior     func(addr uint16, val byte, pc uint16)

	// Phase 2: physical-pool last-writer table, keyed by
	// (bank16k, offset16k) so it survives re-paging — the logical
	// table above goes stale when a slot is remapped (proven on
	// $C02D, where the live $C7 lives in physical bank 111 but the
	// logical record reflects a different bank). Fed by a chained
	// ramWriteHook (successful RAM writes only). The map is allocated
	// at arm time so the hot path never allocates.
	physInstalled bool
	physPrior     func(bank int, addr uint16, val byte)
	phys          map[uint32]provRec
}

// physKey packs a 16K bank index and a 14-bit intra-bank offset into a
// single map key. Offset is masked to 14 bits ($0000-$3FFF), so the
// bank shifts left by 14 with no aliasing across the boundary.
func physKey(bank16k int, off uint16) uint32 {
	return uint32(bank16k)<<14 | uint32(off&0x3FFF)
}

// record stamps the last-writer record for addr. It is a no-op when
// the tracker is disabled so arming/disarming is free on the hot path.
func (p *provenanceTracker) record(addr uint16, val byte, insn uint64, pc uint16, romBank int) {
	if !p.enabled.Load() {
		return
	}
	p.mem[addr] = provRec{valid: true, insn: insn, pc: pc, val: val, romBank: romBank}
}

// lookup returns the last-writer record for a logical address. The
// returned record's valid field is false if nothing has written that
// address since the tracker was armed.
func (p *provenanceTracker) lookup(addr uint16) provRec {
	return p.mem[addr]
}

// recordPhys stamps the last-writer record for a physical pool cell
// (16K bank + intra-bank offset). No-op when disabled or before the
// map is allocated (arm time). The romBank field carries the physical
// 16K bank for display.
func (p *provenanceTracker) recordPhys(bank16k int, off uint16, val byte, insn uint64, pc uint16) {
	if !p.enabled.Load() || p.phys == nil {
		return
	}
	p.phys[physKey(bank16k, off)] = provRec{valid: true, insn: insn, pc: pc, val: val, romBank: bank16k}
}

// lookupPhys returns the last-writer record for a physical pool cell.
func (p *provenanceTracker) lookupPhys(bank16k int, off uint16) provRec {
	if p.phys == nil {
		return provRec{}
	}
	return p.phys[physKey(bank16k, off)]
}

// returnWord is the result of inspecting the return address sitting on
// the stack — the word a RET would (or just did) consume.
type returnWord struct {
	addr   uint16  // the assembled 16-bit value (return target)
	loAddr uint16  // logical address of the low byte
	hiAddr uint16  // logical address of the high byte
	loProv provRec // who last wrote the low byte
	hiProv provRec // who last wrote the high byte
}

// explainPoppedReturn inspects the return word most recently consumed
// by a RET, given the CURRENT SP (i.e. AFTER the RET has incremented
// SP by 2). On the Z80, RET reads lo=[SP], hi=[SP+1] then SP += 2, so
// after the RET the consumed word lives at [SP-2] (lo) and [SP-1]
// (hi). readByte resolves a logical address to its current byte.
//
// This is the heart of `why-pc`: if the assembled addr equals the
// current PC, the jump to PC arrived via RET and loProv/hiProv name
// the instruction that pushed it.
func (p *provenanceTracker) explainPoppedReturn(sp uint16, readByte func(uint16) byte) returnWord {
	lo := sp - 2
	hi := sp - 1
	w := returnWord{
		loAddr: lo,
		hiAddr: hi,
		loProv: p.lookup(lo),
		hiProv: p.lookup(hi),
	}
	w.addr = uint16(readByte(lo)) | uint16(readByte(hi))<<8
	return w
}
