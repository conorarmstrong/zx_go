package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestTraceDBRingOrder verifies the ring keeps the most recent `cap`
// rows and returns them oldest-first.
func TestTraceDBRingOrder(t *testing.T) {
	tb := newTraceDB(3)
	for i := uint64(0); i < 5; i++ {
		tb.record(traceDBRow{insn: i, pc: uint16(0x1000 + i)})
	}
	got := tb.ordered()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (capacity)", len(got))
	}
	// Oldest-first: insns 2,3,4 survived.
	for i, want := range []uint64{2, 3, 4} {
		if got[i].insn != want {
			t.Errorf("row %d insn = %d, want %d", i, got[i].insn, want)
		}
	}
}

// TestTraceDBPartialFill verifies a not-yet-full ring returns just the
// rows written, in order.
func TestTraceDBPartialFill(t *testing.T) {
	tb := newTraceDB(10)
	tb.record(traceDBRow{insn: 7, pc: 0x100})
	tb.record(traceDBRow{insn: 8, pc: 0x200})
	got := tb.ordered()
	if len(got) != 2 || got[0].insn != 7 || got[1].insn != 8 {
		t.Fatalf("ordered = %+v, want insns 7,8", got)
	}
}

// TestTraceDBFlushSQLiteQueryable verifies the flush writes a real
// SQLite db that answers a control-transfer style join — the whole
// point of the tool.
func TestTraceDBFlushSQLiteQueryable(t *testing.T) {
	tb := newTraceDB(8)
	// Two adjacent rows that cross a ROM bank — the pattern the tool
	// is meant to surface.
	tb.record(traceDBRow{insn: 100, pc: 0x3E17, bank: 0, sp: 0xFF56})
	tb.record(traceDBRow{insn: 101, pc: 0x0000, bank: 2, sp: 0xFF56})

	path := filepath.Join(t.TempDir(), "trace.db")
	n, err := tb.flushSQLite(path)
	if err != nil {
		t.Fatalf("flushSQLite: %v", err)
	}
	if n != 2 {
		t.Fatalf("flushed %d rows, want 2", n)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The cross-bank transfer query: adjacent seqs with differing bank.
	var fromPC, toPC, fromBank, toBank int
	row := db.QueryRow(`SELECT a.pc, b.pc, a.bank, b.bank FROM m1 a
	  JOIN m1 b ON b.seq = a.seq + 1 WHERE a.bank <> b.bank`)
	if err := row.Scan(&fromPC, &toPC, &fromBank, &toBank); err != nil {
		t.Fatalf("cross-bank query: %v", err)
	}
	if fromPC != 0x3E17 || toPC != 0x0000 || fromBank != 0 || toBank != 2 {
		t.Errorf("transfer = $%04X(bank %d) -> $%04X(bank %d), want $3E17(0) -> $0000(2)",
			fromPC, fromBank, toPC, toBank)
	}
}

// TestFirstM1Divergence verifies Tool #4's core: the first position
// where two aligned traces differ, naming the field.
func TestFirstM1Divergence(t *testing.T) {
	fields := []string{"pc", "bank", "sp", "af", "bc", "de", "hl", "ix", "iy"}

	a := []traceDBRow{
		{insn: 0, pc: 0x0000},
		{insn: 1, pc: 0x00EF},
		{insn: 2, pc: 0x3E14, bank: 0},
		{insn: 3, pc: 0x3E18, bank: 0},
	}
	// b matches until row 3, where it stays in bank 0 vs a's bank 2.
	b := []traceDBRow{
		{insn: 0, pc: 0x0000},
		{insn: 1, pc: 0x00EF},
		{insn: 2, pc: 0x3E14, bank: 0},
		{insn: 3, pc: 0x3E18, bank: 2}, // bank diverges here
	}
	d := firstM1Divergence(a, b, fields)
	if !d.diverged {
		t.Fatal("expected a divergence")
	}
	if d.seq != 3 || d.field != "bank" {
		t.Errorf("divergence at seq=%d field=%q, want seq=3 field=\"bank\"", d.seq, d.field)
	}

	// Identical traces: no divergence, seq == length.
	d2 := firstM1Divergence(a, a, fields)
	if d2.diverged || d2.seq != len(a) {
		t.Errorf("identical traces = %+v, want diverged=false seq=%d", d2, len(a))
	}

	// Earliest field/position wins: a PC mismatch at row 1 beats the
	// bank mismatch at row 3.
	c := append([]traceDBRow(nil), b...)
	c[1].pc = 0x1234
	d3 := firstM1Divergence(a, c, fields)
	if d3.seq != 1 || d3.field != "pc" {
		t.Errorf("got seq=%d field=%q, want seq=1 field=\"pc\"", d3.seq, d3.field)
	}
}

// TestLoadM1RoundTrip verifies flush→load preserves rows in order so
// the divergence finder can consume a DB written by another run.
func TestLoadM1RoundTrip(t *testing.T) {
	tb := newTraceDB(4)
	tb.record(traceDBRow{insn: 10, pc: 0x1111, bank: 1, sp: 0xFE00, hl: 0x2222})
	tb.record(traceDBRow{insn: 11, pc: 0x3333, bank: 2, sp: 0xFDFE, ix: 0x4444})
	path := filepath.Join(t.TempDir(), "rt.db")
	if _, err := tb.flushSQLite(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := loadM1Rows(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].pc != 0x1111 || got[0].hl != 0x2222 || got[1].ix != 0x4444 {
		t.Fatalf("loaded = %+v, want the two written rows", got)
	}
}

// TestTraceDBOverlayColumns verifies the flush carries the per-row
// overlay context: alt (NR$8C alt-rom register) and dmc (divMMC
// paged-in) — without these, a logical PC recorded while an overlay
// was active cannot be attributed to the code that actually executed.
func TestTraceDBOverlayColumns(t *testing.T) {
	tb := newTraceDB(4)
	tb.record(traceDBRow{insn: 1, pc: 0x0136, bank: 0, alt: 0x80, dmc: 0})
	tb.record(traceDBRow{insn: 2, pc: 0x0136, bank: 0, alt: 0x00, dmc: 1})

	path := filepath.Join(t.TempDir(), "trace.db")
	if _, err := tb.flushSQLite(path); err != nil {
		t.Fatalf("flushSQLite: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var alt1, dmc1, alt2, dmc2 int
	if err := db.QueryRow(`SELECT alt, dmc FROM m1 WHERE insn=1`).Scan(&alt1, &dmc1); err != nil {
		t.Fatalf("row 1: %v", err)
	}
	if err := db.QueryRow(`SELECT alt, dmc FROM m1 WHERE insn=2`).Scan(&alt2, &dmc2); err != nil {
		t.Fatalf("row 2: %v", err)
	}
	if alt1 != 0x80 || dmc1 != 0 || alt2 != 0x00 || dmc2 != 1 {
		t.Errorf("overlay cols = (%#02x,%d) (%#02x,%d), want (0x80,0) (0x00,1)",
			alt1, dmc1, alt2, dmc2)
	}
}

// TestTraceDBFrameColumn verifies the flush carries the frame number —
// the only honest era anchor (insn counts alone conflate boot-wipe
// loops with menu idle).
func TestTraceDBFrameColumn(t *testing.T) {
	tb := newTraceDB(4)
	tb.record(traceDBRow{insn: 1, pc: 0x1000, frame: 1999})
	tb.record(traceDBRow{insn: 2, pc: 0x2000, frame: 3500})

	path := filepath.Join(t.TempDir(), "trace.db")
	if _, err := tb.flushSQLite(path); err != nil {
		t.Fatalf("flushSQLite: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var fr int
	if err := db.QueryRow(`SELECT frame FROM m1 WHERE insn=2`).Scan(&fr); err != nil {
		t.Fatalf("frame col: %v", err)
	}
	if fr != 3500 {
		t.Errorf("frame = %d, want 3500", fr)
	}
}
