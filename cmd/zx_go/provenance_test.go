package main

import "testing"

// TestProvenanceRecordsLastWriter verifies the core contract: after a
// write is recorded, lookup returns the writing instruction's PC and
// counter.
func TestProvenanceRecordsLastWriter(t *testing.T) {
	p := &provenanceTracker{}
	p.enabled.Store(true)

	p.record(0xFF54, 0x00, 21401930, 0x6D02, 0)

	got := p.lookup(0xFF54)
	if !got.valid {
		t.Fatal("lookup($FF54) not valid after record")
	}
	if got.pc != 0x6D02 || got.insn != 21401930 || got.val != 0x00 {
		t.Errorf("lookup = pc=$%04X insn=%d val=$%02X, want pc=$6D02 insn=21401930 val=$00",
			got.pc, got.insn, got.val)
	}
}

// TestProvenanceLastWriterWins verifies that a later write to the same
// address overwrites the earlier record — the most recent push to a
// stack slot is the one a RET pops.
func TestProvenanceLastWriterWins(t *testing.T) {
	p := &provenanceTracker{}
	p.enabled.Store(true)

	p.record(0x5B00, 0x11, 100, 0x1000, 0)
	p.record(0x5B00, 0x22, 200, 0x2000, 1)

	got := p.lookup(0x5B00)
	if got.insn != 200 || got.pc != 0x2000 || got.val != 0x22 || got.romBank != 1 {
		t.Errorf("lookup = %+v, want the second (insn=200, pc=$2000, val=$22, romBank=1)", got)
	}
}

// TestProvenanceDisabledIsNoOp verifies arming is free: while disabled,
// record does nothing so the hot-path hook is a no-op.
func TestProvenanceDisabledIsNoOp(t *testing.T) {
	p := &provenanceTracker{} // enabled defaults to false
	p.record(0x8000, 0xAB, 5, 0x1234, 0)
	if p.lookup(0x8000).valid {
		t.Error("record while disabled should be a no-op")
	}
}

// TestProvenancePhysRecordsLastWriter verifies the Phase-2 physical
// keying: a write to a physical pool cell (bank16k, offset) is keyed
// independently of the logical address, so it survives re-paging.
func TestProvenancePhysRecordsLastWriter(t *testing.T) {
	p := &provenanceTracker{}
	p.enabled.Store(true)
	p.phys = make(map[uint32]provRec)

	// $C7 written to physical bank 111, offset $002D.
	p.recordPhys(111, 0x002D, 0xC7, 999, 0x4321)

	got := p.lookupPhys(111, 0x002D)
	if !got.valid || got.val != 0xC7 || got.pc != 0x4321 || got.insn != 999 {
		t.Errorf("lookupPhys(111,$002D) = %+v, want val=$C7 pc=$4321 insn=999", got)
	}
	// A different bank at the same offset is a distinct cell.
	if p.lookupPhys(5, 0x002D).valid {
		t.Error("bank 5 offset $002D should be untouched")
	}
}

// TestProvenancePhysKeyDistinct verifies the physical key separates
// (bank, offset) pairs without collision across the 14-bit offset
// boundary.
func TestProvenancePhysKeyDistinct(t *testing.T) {
	if physKey(0, 0x4000) == physKey(1, 0x0000) {
		t.Error("physKey must not alias bank 1 offset 0 with bank 0 offset $4000")
	}
	if physKey(111, 0x002D) == physKey(110, 0x002D) {
		t.Error("physKey must distinguish adjacent banks")
	}
}

// TestProvenancePhysDisabledIsNoOp verifies physical recording honours
// the enable gate.
func TestProvenancePhysDisabledIsNoOp(t *testing.T) {
	p := &provenanceTracker{}
	p.phys = make(map[uint32]provRec)
	p.recordPhys(111, 0x002D, 0xC7, 1, 0x1000) // disabled
	if p.lookupPhys(111, 0x002D).valid {
		t.Error("recordPhys while disabled should be a no-op")
	}
}

// TestProvenanceExplainPoppedReturn verifies the why-pc heart: given
// the SP after a RET, it reads the consumed return word at [SP-2,SP-1]
// and pairs each byte with its last-writer record.
func TestProvenanceExplainPoppedReturn(t *testing.T) {
	p := &provenanceTracker{}
	p.enabled.Store(true)

	// A PUSH at insn 500 / PC=$6D02 wrote the return word $0000 to the
	// stack: lo byte at $FF54, hi byte at $FF55.
	p.record(0xFF54, 0x00, 500, 0x6D02, 0)
	p.record(0xFF55, 0x00, 500, 0x6D02, 0)

	mem := map[uint16]byte{0xFF54: 0x00, 0xFF55: 0x00}
	read := func(a uint16) byte { return mem[a] }

	// After the RET, SP has advanced past the word: SP = $FF56.
	w := p.explainPoppedReturn(0xFF56, read)

	if w.loAddr != 0xFF54 || w.hiAddr != 0xFF55 {
		t.Errorf("addrs = lo=$%04X hi=$%04X, want lo=$FF54 hi=$FF55", w.loAddr, w.hiAddr)
	}
	if w.addr != 0x0000 {
		t.Errorf("assembled return addr = $%04X, want $0000", w.addr)
	}
	if !w.loProv.valid || w.loProv.pc != 0x6D02 || w.loProv.insn != 500 {
		t.Errorf("loProv = %+v, want pc=$6D02 insn=500", w.loProv)
	}
	if !w.hiProv.valid || w.hiProv.pc != 0x6D02 {
		t.Errorf("hiProv = %+v, want pc=$6D02", w.hiProv)
	}
}
