package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// tracedb.go implements Tool #2: a queryable execution-trace database.
// Instead of grepping multi-million-line JSONL traces by hand, every
// M1 fetch is recorded into a bounded in-memory ring and flushed to a
// SQLite file at the end of the run. You then run arbitrary SQL:
//
//	-- every control transfer that crossed a ROM bank in the window
//	SELECT a.insn, a.pc, a.bank, b.pc, b.bank FROM m1 a JOIN m1 b
//	  ON b.seq = a.seq+1 WHERE a.bank <> b.bank;
//
//	-- where did SP collapse below the stack floor?
//	SELECT seq, insn, printf('$%04X', pc), printf('$%04X', sp)
//	  FROM m1 WHERE sp < 0x4000 ORDER BY seq LIMIT 20;
//
// The ring bounds memory: the most recent `cap` fetches are kept,
// which is what matters for diagnosing a trap (the tail leading into
// it). Pure-Go SQLite keeps it cgo-free and CI-friendly.

// traceDBRow is one recorded M1 fetch. Kept flat + small so the
// hot-path append is cheap.
type traceDBRow struct {
	insn           uint64
	pc, sp         uint16
	af, bc, de, hl uint16
	ix, iy         uint16
	bank           int
	alt            int // NR$8C alt-rom register at fetch (overlay attribution)
	dmc            int // 1 when the divMMC overlay was paged in
	frame          int // headless frame number (the only honest era anchor)
}

// traceDB is a fixed-capacity ring of M1 records. When full, the
// oldest record is overwritten — so it always holds the most recent
// `cap` fetches.
type traceDB struct {
	rows []traceDBRow
	cap  int
	head int  // next write index
	full bool // wrapped at least once
}

// newTraceDB allocates a ring of the given capacity. A non-positive
// capacity yields nil (disabled).
func newTraceDB(capacity int) *traceDB {
	if capacity <= 0 {
		return nil
	}
	return &traceDB{rows: make([]traceDBRow, capacity), cap: capacity}
}

// record appends a row, evicting the oldest when full.
func (t *traceDB) record(r traceDBRow) {
	t.rows[t.head] = r
	t.head++
	if t.head == t.cap {
		t.head = 0
		t.full = true
	}
}

// ordered returns the buffered rows oldest-first.
func (t *traceDB) ordered() []traceDBRow {
	if !t.full {
		out := make([]traceDBRow, t.head)
		copy(out, t.rows[:t.head])
		return out
	}
	out := make([]traceDBRow, 0, t.cap)
	out = append(out, t.rows[t.head:]...)
	out = append(out, t.rows[:t.head]...)
	return out
}

// flushSQLite writes the buffered rows to a SQLite file at path,
// creating the `m1` table. A monotonically increasing `seq` column
// preserves execution order so adjacent-row joins (transfers) work.
// All inserts run in one transaction for speed.
func (t *traceDB) flushSQLite(path string) (int, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	schema := `
DROP TABLE IF EXISTS m1;
CREATE TABLE m1 (
  seq  INTEGER PRIMARY KEY,
  insn INTEGER, pc INTEGER, bank INTEGER, alt INTEGER, dmc INTEGER, frame INTEGER, sp INTEGER,
  af INTEGER, bc INTEGER, de INTEGER, hl INTEGER, ix INTEGER, iy INTEGER
);`
	if _, err := db.Exec(schema); err != nil {
		return 0, fmt.Errorf("create schema: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO m1
	  (seq, insn, pc, bank, alt, dmc, frame, sp, af, bc, de, hl, ix, iy)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	rows := t.ordered()
	for i, r := range rows {
		if _, err := stmt.Exec(i, int64(r.insn), int(r.pc), r.bank, r.alt, r.dmc, r.frame, int(r.sp),
			int(r.af), int(r.bc), int(r.de), int(r.hl), int(r.ix), int(r.iy)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return 0, fmt.Errorf("insert row %d: %w", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// m1Compare is one aligned pair of trace rows for divergence reporting.
type m1Compare struct {
	seq      int
	a, b     traceDBRow
	field    string // the first field that differs ("pc", "bank", "sp", "af", …)
	diverged bool
}

// rowField returns the named field's value for divergence reporting.
func rowField(r traceDBRow, name string) uint64 {
	switch name {
	case "pc":
		return uint64(r.pc)
	case "bank":
		return uint64(r.bank)
	case "sp":
		return uint64(r.sp)
	case "af":
		return uint64(r.af)
	case "bc":
		return uint64(r.bc)
	case "de":
		return uint64(r.de)
	case "hl":
		return uint64(r.hl)
	case "ix":
		return uint64(r.ix)
	case "iy":
		return uint64(r.iy)
	}
	return 0
}

// firstM1Divergence walks two aligned trace-row slices (oldest-first,
// e.g. from two emulators stepped in lockstep) and returns the first
// position where any compared field differs. compareFields lists the
// fields to check in priority order; the returned m1Compare names the
// first field that differed. diverged=false means the common prefix
// matched entirely (one may be longer).
//
// This is the core of Tool #4: feed it our trace and an oracle trace
// (e.g. the GHDL gate-level reference) and it pinpoints the exact
// instruction where the emulation first deviates from ground truth.
func firstM1Divergence(a, b []traceDBRow, compareFields []string) m1Compare {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		for _, f := range compareFields {
			if rowField(a[i], f) != rowField(b[i], f) {
				return m1Compare{seq: i, a: a[i], b: b[i], field: f, diverged: true}
			}
		}
	}
	return m1Compare{seq: n, diverged: false}
}

// loadM1Rows reads the m1 table from a trace DB, ordered by seq.
func loadM1Rows(db *sql.DB) ([]traceDBRow, error) {
	rows, err := db.Query(`SELECT insn, pc, bank, sp, af, bc, de, hl, ix, iy
	  FROM m1 ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []traceDBRow
	for rows.Next() {
		var r traceDBRow
		var insn int64
		var pc, bank, sp, af, bc, de, hl, ix, iy int
		if err := rows.Scan(&insn, &pc, &bank, &sp, &af, &bc, &de, &hl, &ix, &iy); err != nil {
			return nil, err
		}
		r.insn = uint64(insn)
		r.pc, r.sp = uint16(pc), uint16(sp)
		r.af, r.bc, r.de, r.hl = uint16(af), uint16(bc), uint16(de), uint16(hl)
		r.ix, r.iy = uint16(ix), uint16(iy)
		r.bank = bank
		out = append(out, r)
	}
	return out, rows.Err()
}
