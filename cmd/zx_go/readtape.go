package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// readTape records a VALUE-level "read tape" for first-divergence-by-
// value analysis (the instruction-level lockstep facility). From a
// start checkpoint — the startHit-th execution of startPC — every RAM
// read is logged as one line:
//
//	SEQ R bBANK:OFF =VAL @PC
//
// e.g. `0 R b0:1f4f =c7 @27aa`. The insight: once two deterministic
// Z80s share an IDENTICAL machine state at a checkpoint, they can
// only diverge because a read returned a different byte
// (registers can't change spontaneously; only inputs can). So the FIRST
// tape line whose VALUE differs between our run and the oracle's run is
// the functional root cause — no bisection, no guessing in the gap.
//
// The oracle's tape is captured ONCE (a reference-emulator plugin hooking
// Memory_Read/Port_Read/NextReg_Read from the same trigger PC); thereafter
// it is a pure local diff (tapediff).
//
// This reuses the existing memory RAM-read hook (SetRAMReadHook) and the
// CPU pre-fetch hook (AddPreFetchHook); it is wired only when
// --read-tape is set, so there is zero hot-path cost otherwise.
//
// v1 captures RAM reads (physical bank16k + offset + value) — the reads
// most likely to diverge in the bank-2 file-load path (sector buffers,
// sysvars, computed tables), since ROM reads are identical by
// construction. Port-IN and NextReg reads are a planned extension
// (taps in wire.ReadPort / dispatcher.read).
type readTape struct {
	w        *bufio.Writer
	startPC  uint16
	startHit int
	max      uint64

	hits  int
	armed bool
	seq   uint64
	pc    uint16 // last executed PC, for line annotation
	done  bool
}

// newReadTape builds a recorder writing to w. Recording arms at the
// startHit-th execution of startPC; with startPC==0 && startHit<=0 it
// arms immediately. max caps the number of recorded lines (0 = no cap).
func newReadTape(w io.Writer, startPC uint16, startHit int, max uint64) *readTape {
	rt := &readTape{w: bufio.NewWriter(w), startPC: startPC, startHit: startHit, max: max}
	if startPC == 0 && startHit <= 0 {
		rt.armed = true
	}
	return rt
}

// onExecute marks an instruction boundary (wire to AddPreFetchHook). It
// counts executions of startPC and arms once the startHit-th is seen.
func (rt *readTape) onExecute(pc uint16) {
	rt.pc = pc
	if rt.armed || rt.done {
		return
	}
	if pc == rt.startPC {
		rt.hits++
		if rt.hits >= rt.startHit {
			rt.armed = true
		}
	}
}

// onRead records one RAM read (wire to SetRAMReadHook). bank16k is the
// physical 16K bank, off the byte offset within it, val the byte read.
func (rt *readTape) onRead(bank16k int, off uint16, val byte) {
	if !rt.armed || rt.done {
		return
	}
	_, _ = fmt.Fprintf(rt.w, "%d R b%d:%04x =%02x @%04x\n", rt.seq, bank16k, off, val, rt.pc)
	rt.seq++
	if rt.max > 0 && rt.seq >= rt.max {
		rt.done = true
	}
}

// onReadAll records one read of ANY address (logical addr + value),
// for --read-tape-all bank-agnostic lockstep. The address coordinate
// is logical ("a<addr>") rather than physical bank:off, but readtapediff
// only compares the =VAL field, so it diffs cleanly against a reference
// emulator that logs every read by logical address (e.g. a reference emulator Lua
// read-tap). ROM reads match by construction; the first divergent value
// is the functional root cause regardless of code banking.
func (rt *readTape) onReadAll(addr uint16, val byte) {
	if !rt.armed || rt.done {
		return
	}
	_, _ = fmt.Fprintf(rt.w, "%d R a%04x =%02x @%04x\n", rt.seq, addr, val, rt.pc)
	rt.seq++
	if rt.max > 0 && rt.seq >= rt.max {
		rt.done = true
	}
}

// onPortWrite records one guest OUT (wire to the ULA port tracer,
// write side). Port-level records are what disambiguate
// multi-occupant RAM code in oracle diffs.
func (rt *readTape) onPortWrite(port uint16, val byte) {
	if !rt.armed || rt.done {
		return
	}
	_, _ = fmt.Fprintf(rt.w, "%d W p%04x =%02x @%04x\n", rt.seq, port, val, rt.pc)
	rt.seq++
	if rt.max > 0 && rt.seq >= rt.max {
		rt.done = true
	}
}

// onPortRead records one guest IN (wire to the ULA port tracer,
// read side). Together with onPortWrite this makes polling loops
// directly observable in oracle diffs.
func (rt *readTape) onPortRead(port uint16, val byte) {
	if !rt.armed || rt.done {
		return
	}
	_, _ = fmt.Fprintf(rt.w, "%d I p%04x =%02x @%04x\n", rt.seq, port, val, rt.pc)
	rt.seq++
	if rt.max > 0 && rt.seq >= rt.max {
		rt.done = true
	}
}

// flush writes any buffered tape lines to the underlying writer.
func (rt *readTape) flush() { _ = rt.w.Flush() }

// parseReadTapeFrom parses the --read-tape-from value "$PC[:hit]" into
// the arming PC and hit count. Empty → (0, 0) = record from boot. A
// bare "$PC" defaults to the 1st hit. Unparseable values fall back to
// (0, 0) so a typo records from boot rather than silently never arming.
func parseReadTapeFrom(spec string) (startPC uint16, startHit int) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0
	}
	pcPart := spec
	hit := 1
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		pcPart = spec[:i]
		if n, err := strconv.Atoi(strings.TrimSpace(spec[i+1:])); err == nil && n > 0 {
			hit = n
		}
	}
	pc, err := parseHex(strings.TrimSpace(pcPart))
	if err != nil {
		return 0, 0
	}
	return pc, hit
}
