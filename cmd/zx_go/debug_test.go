package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseWatchWriteSpec(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []watchWriteEntry
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "single log entry",
			in:   "ERR_NR@5C3A",
			want: []watchWriteEntry{{name: "ERR_NR", addr: 0x5C3A}},
		},
		{
			name: "colon separator (back-compat)",
			in:   "ERR_NR:5C3A",
			want: []watchWriteEntry{{name: "ERR_NR", addr: 0x5C3A}},
		},
		{
			name: "break-on-value form",
			in:   "BUG@E579=15",
			want: []watchWriteEntry{
				{name: "BUG", addr: 0xE579, breakValue: 0x15, hasBreak: true},
			},
		},
		{
			name: "value with $ prefix",
			in:   "BUG@E579=$15",
			want: []watchWriteEntry{
				{name: "BUG", addr: 0xE579, breakValue: 0x15, hasBreak: true},
			},
		},
		{
			name: "multiple entries mixed",
			in:   "A@1000,B@2000=42,C@3000",
			want: []watchWriteEntry{
				{name: "A", addr: 0x1000},
				{name: "B", addr: 0x2000, breakValue: 0x42, hasBreak: true},
				{name: "C", addr: 0x3000},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseWatchWriteSpec(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseWatchWriteSpec(%q)\n got=%+v\nwant=%+v", c.in, got, c.want)
			}
		})
	}
}

func TestParseDumpMemSpec(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []dumpMemRange
	}{
		{name: "empty", in: "", want: nil},
		{
			name: "single",
			in:   "E570:20",
			want: []dumpMemRange{{addr: 0xE570, length: 0x20, bank: -1, rom: -1}},
		},
		{
			name: "two",
			in:   "E570:20,5C3A:08",
			want: []dumpMemRange{
				{addr: 0xE570, length: 0x20, bank: -1, rom: -1},
				{addr: 0x5C3A, length: 0x08, bank: -1, rom: -1},
			},
		},
		{
			name: "$ prefix tolerated",
			in:   "$E570:$10",
			want: []dumpMemRange{{addr: 0xE570, length: 0x10, bank: -1, rom: -1}},
		},
		{
			name: "bank prefix",
			in:   "bank7:0000:200",
			want: []dumpMemRange{{addr: 0x0000, length: 0x200, bank: 7, rom: -1}},
		},
		{
			name: "rom prefix",
			in:   "rom3:0000:40",
			want: []dumpMemRange{{addr: 0x0000, length: 0x40, bank: -1, rom: 3}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseDumpMemSpec(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseDumpMemSpec(%q)\n got=%+v\nwant=%+v", c.in, got, c.want)
			}
		})
	}
}

// ========================================================================
// Symbol map tests.
// ========================================================================

// resetSymbolTable clears the package-global symbolTable between tests
// so the shared global doesn't leak state across tests.
func resetSymbolTable() {
	symbolMu.Lock()
	symbolTable = nil
	symbolMu.Unlock()
}

// TestLoadSymbolMapParsesEntries — addresses with `$` and `0x` prefixes
// plus bare hex, multi-word names joined, comments and blanks skipped.
func TestLoadSymbolMapParsesEntries(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()

	dir := t.TempDir()
	path := filepath.Join(dir, "syms.map")
	content := `# leading comment
; semicolon comment also ignored
$1234 firstSymbol
0x5678 SECOND
ABCD third symbol with spaces

# blank line above, comment here
BEEF
NOTHEX something
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	loadSymbolMap(path)

	want := map[uint16]string{
		0x1234: "firstSymbol",
		0x5678: "SECOND",
		0xABCD: "third symbol with spaces",
	}
	symbolMu.RLock()
	got := symbolTable
	symbolMu.RUnlock()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("symbolTable = %+v, want %+v", got, want)
	}
}

func TestLoadSymbolMapEmptyPathNoop(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()
	// Pre-populate so we can confirm loadSymbolMap("") doesn't clear.
	addSymbol(0x1234, "preserved")
	loadSymbolMap("")
	if v := annotateAddr(0x1234); v != "$1234 (preserved)" {
		t.Errorf("annotateAddr after empty-path load = %q, want preserved", v)
	}
}

func TestLoadSymbolMapMissingFile(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()
	// Pre-populate to confirm a failed open leaves old table intact.
	addSymbol(0x1234, "preserved")
	loadSymbolMap("/nonexistent/path/that/should/not/exist")
	if v := annotateAddr(0x1234); v != "$1234 (preserved)" {
		t.Errorf("annotateAddr after failed load = %q, want preserved", v)
	}
}

// TestLoadSymbolMapReplacesPriorTable — second call must replace,
// not merge. This is the contract of reload-syms PATH at runtime.
func TestLoadSymbolMapReplacesPriorTable(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()

	dir := t.TempDir()
	first := filepath.Join(dir, "a.map")
	second := filepath.Join(dir, "b.map")
	_ = os.WriteFile(first, []byte("1000 from_a\n"), 0o644)
	_ = os.WriteFile(second, []byte("2000 from_b\n"), 0o644)

	loadSymbolMap(first)
	if v := annotateAddr(0x1000); v != "$1000 (from_a)" {
		t.Fatalf("after first load: annotateAddr($1000) = %q", v)
	}
	loadSymbolMap(second)
	if v := annotateAddr(0x1000); v != "$1000" {
		t.Errorf("after replace: annotateAddr($1000) = %q, want plain (table replaced)", v)
	}
	if v := annotateAddr(0x2000); v != "$2000 (from_b)" {
		t.Errorf("after replace: annotateAddr($2000) = %q", v)
	}
}

// TestAnnotateAddrNoTable — bare address when nothing loaded.
func TestAnnotateAddrNoTable(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()
	if v := annotateAddr(0xDEAD); v != "$DEAD" {
		t.Errorf("annotateAddr with no table = %q, want $DEAD", v)
	}
}

// TestAddClearListSymbols — runtime mutation API.
func TestAddClearListSymbols(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()

	addSymbol(0x4000, "screen")
	addSymbol(0x5800, "attrs")
	addSymbol(0x5C3A, "ERR_NR")

	got := listSymbols()
	if len(got) != 3 {
		t.Fatalf("listSymbols returned %d entries, want 3", len(got))
	}
	// Sorted by address: $4000, $5800, $5C3A.
	if got[0].Addr != 0x4000 || got[1].Addr != 0x5800 || got[2].Addr != 0x5C3A {
		t.Errorf("listSymbols not sorted by address: %+v", got)
	}

	clearSymbol(0x5800)
	got = listSymbols()
	if len(got) != 2 {
		t.Fatalf("after clear: %d entries, want 2", len(got))
	}
	if got[0].Addr != 0x4000 || got[1].Addr != 0x5C3A {
		t.Errorf("after clear, addresses = %v %v", got[0].Addr, got[1].Addr)
	}

	// clearSymbol on a non-existent entry must not panic.
	clearSymbol(0xBEEF)
}

// TestAddSymbolOverwrites — same address replaces the prior name.
func TestAddSymbolOverwrites(t *testing.T) {
	resetSymbolTable()
	defer resetSymbolTable()
	addSymbol(0x1234, "first")
	addSymbol(0x1234, "second")
	if v := annotateAddr(0x1234); v != "$1234 (second)" {
		t.Errorf("annotateAddr after overwrite = %q, want second", v)
	}
}

// ========================================================================
// parsePCTriggers tests.
// ========================================================================

func TestParsePCTriggersEmpty(t *testing.T) {
	tr := parsePCTriggers("", func(string, uint16) {})
	if tr != nil {
		t.Errorf("parsePCTriggers(\"\") = %v, want nil", tr)
	}
}

func TestParsePCTriggersSinglePoint(t *testing.T) {
	tr := parsePCTriggers("entry@$1234", func(string, uint16) {})
	if tr == nil || len(tr.ranges) != 1 {
		t.Fatalf("ranges = %+v", tr)
	}
	r := tr.ranges[0]
	if r.name != "entry" || r.lo != 0x1234 || r.hi != 0x1234 {
		t.Errorf("parsed range = %+v", r)
	}
}

func TestParsePCTriggersRange(t *testing.T) {
	tr := parsePCTriggers("rom@$0000-$3FFF", func(string, uint16) {})
	if tr == nil || len(tr.ranges) != 1 {
		t.Fatalf("ranges = %+v", tr)
	}
	r := tr.ranges[0]
	if r.name != "rom" || r.lo != 0x0000 || r.hi != 0x3FFF {
		t.Errorf("parsed range = %+v", r)
	}
}

func TestParsePCTriggersColonSeparator(t *testing.T) {
	// Code uses `@` preferentially, falls back to `:` if `@` missing.
	tr := parsePCTriggers("welcome:1234", func(string, uint16) {})
	if tr == nil || len(tr.ranges) != 1 {
		t.Fatalf("ranges = %+v", tr)
	}
	if tr.ranges[0].lo != 0x1234 {
		t.Errorf("colon parse lo = %04X, want 1234", tr.ranges[0].lo)
	}
}

func TestParsePCTriggersMultipleAndMalformed(t *testing.T) {
	tr := parsePCTriggers("a@1000,bad-no-separator,b@2000-3000", func(string, uint16) {})
	if len(tr.ranges) != 2 {
		t.Fatalf("ranges = %+v, want 2 (malformed dropped)", tr.ranges)
	}
	if tr.ranges[0].name != "a" || tr.ranges[0].lo != 0x1000 || tr.ranges[0].hi != 0x1000 {
		t.Errorf("first range = %+v", tr.ranges[0])
	}
	if tr.ranges[1].name != "b" || tr.ranges[1].lo != 0x2000 || tr.ranges[1].hi != 0x3000 {
		t.Errorf("second range = %+v", tr.ranges[1])
	}
}

func TestPCTriggerStepFiresOnceInRange(t *testing.T) {
	var fired []struct {
		name string
		pc   uint16
	}
	tr := parsePCTriggers("entry@$0100-$0200,exit@$8000", func(name string, pc uint16) {
		fired = append(fired, struct {
			name string
			pc   uint16
		}{name, pc})
	})

	// Walking PC through entry range many times — should fire ONCE.
	for pc := uint16(0x0100); pc <= 0x0200; pc += 0x10 {
		tr.Step(pc)
	}
	if len(fired) != 1 || fired[0].name != "entry" || fired[0].pc != 0x0100 {
		t.Errorf("entry fire = %+v, want one fire at $0100", fired)
	}

	// PC outside any range — no additional fire.
	tr.Step(0x0500)
	if len(fired) != 1 {
		t.Errorf("after non-range step: %+v", fired)
	}

	// Hitting the second trigger.
	tr.Step(0x8000)
	if len(fired) != 2 || fired[1].name != "exit" {
		t.Errorf("after second range: %+v", fired)
	}
}

// ========================================================================
// loopDetector tests.
// ========================================================================

func TestNewLoopDetectorZeroThresholdReturnsNil(t *testing.T) {
	if ld := newLoopDetector(0, func(uint16, int) {}); ld != nil {
		t.Errorf("newLoopDetector(0) = %v, want nil", ld)
	}
	if ld := newLoopDetector(-1, func(uint16, int) {}); ld != nil {
		t.Errorf("newLoopDetector(-1) = %v, want nil", ld)
	}
}

func TestLoopDetectorFiresAtThreshold(t *testing.T) {
	var fired []struct {
		pc    uint16
		count int
	}
	ld := newLoopDetector(3, func(pc uint16, count int) {
		fired = append(fired, struct {
			pc    uint16
			count int
		}{pc, count})
	})

	// PC=$1234 four times in a row — should fire once at threshold=3.
	for i := 0; i < 4; i++ {
		ld.Step(0x1234)
	}
	if len(fired) != 1 {
		t.Fatalf("fired = %+v, want exactly one", fired)
	}
	if fired[0].pc != 0x1234 || fired[0].count != 3 {
		t.Errorf("fire = %+v, want pc=$1234 count=3", fired[0])
	}
}

func TestLoopDetectorRearmsOnPCChange(t *testing.T) {
	var fired int
	ld := newLoopDetector(2, func(uint16, int) { fired++ })

	// First stall.
	ld.Step(0x1000)
	ld.Step(0x1000) // fires
	if fired != 1 {
		t.Fatalf("first stall: fired=%d, want 1", fired)
	}

	// Many more at same PC — must NOT re-fire.
	for i := 0; i < 5; i++ {
		ld.Step(0x1000)
	}
	if fired != 1 {
		t.Fatalf("after extra-same-PC steps: fired=%d, want still 1", fired)
	}

	// PC moves — re-arms.
	ld.Step(0x2000)
	ld.Step(0x2000) // fires at new PC
	if fired != 2 {
		t.Errorf("after re-arm + new stall: fired=%d, want 2", fired)
	}
}

func TestLoopDetectorDoesNotFireBelowThreshold(t *testing.T) {
	var fired int
	ld := newLoopDetector(5, func(uint16, int) { fired++ })

	// 4 reps at threshold=5 — must NOT fire.
	for i := 0; i < 4; i++ {
		ld.Step(0x1234)
	}
	if fired != 0 {
		t.Errorf("below threshold: fired=%d, want 0", fired)
	}

	// 5th rep — does fire.
	ld.Step(0x1234)
	if fired != 1 {
		t.Errorf("at threshold: fired=%d, want 1", fired)
	}
}

// ========================================================================
// parseWatchSpec tests.
// ========================================================================

func TestParseWatchSpecEmptyReturnsDefault(t *testing.T) {
	got := parseWatchSpec("")
	def := defaultNextWatch()
	if !reflect.DeepEqual(got, def) {
		t.Errorf("parseWatchSpec(\"\") = %+v, want default %+v", got, def)
	}
	if len(got) < 5 {
		t.Errorf("default watch set is suspiciously small: %d entries", len(got))
	}
}

func TestParseWatchSpecSingleByte(t *testing.T) {
	got := parseWatchSpec("ERR_NR:5C3A")
	want := []watchEntry{{name: "ERR_NR", addr: 0x5C3A, word: false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWatchSpec single = %+v, want %+v", got, want)
	}
}

func TestParseWatchSpecWordSuffix(t *testing.T) {
	got := parseWatchSpec("PROG:5C53:w")
	want := []watchEntry{{name: "PROG", addr: 0x5C53, word: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWatchSpec word-suffix = %+v, want %+v", got, want)
	}
}

func TestParseWatchSpecHexPrefixes(t *testing.T) {
	got := parseWatchSpec("A:$1234,B:0x5678,C:ABCD")
	want := []watchEntry{
		{name: "A", addr: 0x1234},
		{name: "B", addr: 0x5678},
		{name: "C", addr: 0xABCD},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWatchSpec prefixes = %+v, want %+v", got, want)
	}
}

func TestParseWatchSpecSkipsMalformed(t *testing.T) {
	got := parseWatchSpec("good:1000,bad-no-colon,also-good:2000")
	want := []watchEntry{
		{name: "good", addr: 0x1000},
		{name: "also-good", addr: 0x2000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWatchSpec skip-malformed = %+v, want %+v", got, want)
	}
}

// ========================================================================
// Small pure helpers.
// ========================================================================

func TestFormatHexBytes(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{nil, ""},
		{[]byte{}, ""},
		{[]byte{0}, "00"},
		{[]byte{0xFF, 0x00, 0xAB}, "FF 00 AB"},
		{[]byte{0xDE, 0xAD, 0xBE, 0xEF}, "DE AD BE EF"},
	}
	for _, c := range cases {
		got := formatHexBytes(c.in)
		if got != c.want {
			t.Errorf("formatHexBytes(%x) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatHex16(t *testing.T) {
	cases := []struct {
		in   uint16
		want string
	}{
		{0x0000, "0000"},
		{0xFFFF, "FFFF"},
		{0x1234, "1234"},
		{0xABCD, "ABCD"},
		{0x000F, "000F"},
		{0xF000, "F000"},
	}
	for _, c := range cases {
		got := formatHex16(c.in)
		if got != c.want {
			t.Errorf("formatHex16($%04X) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\r\nb\r\nc"); got != "a | b | c" {
		t.Errorf("CRLF: got %q", got)
	}
	if got := oneLine("a\nb"); got != "a | b" {
		t.Errorf("LF: got %q", got)
	}
	if got := oneLine("plain"); got != "plain" {
		t.Errorf("plain: got %q", got)
	}
	// Truncation at 200 chars.
	long := strings.Repeat("x", 250)
	got := oneLine(long)
	if len(got) != 200 {
		t.Errorf("truncated length = %d, want 200", len(got))
	}
	if got[197:] != "..." {
		t.Errorf("truncated tail = %q, want ...", got[197:])
	}
}

func TestParseHex(t *testing.T) {
	cases := []struct {
		in      string
		want    uint16
		wantErr bool
	}{
		{"1234", 0x1234, false},
		{"$ABCD", 0xABCD, false},
		{"0xDEAD", 0xDEAD, false},
		{"  $BEEF  ", 0xBEEF, false},
		{"FFFF", 0xFFFF, false},
		{"0", 0, false},
		{"NOTHEX", 0, true},
		{"", 0, true},
		{"10000", 0, true}, // overflow 16-bit
	}
	for _, c := range cases {
		got, err := parseHex(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseHex(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseHex(%q) = $%04X, want $%04X", c.in, got, c.want)
		}
	}
}

func TestParseFirstEntryRange(t *testing.T) {
	cases := []struct {
		in       string
		wantLo   uint16
		wantHi   uint16
		wantErr  bool
		errLabel string
	}{
		{"$1234", 0x1234, 0x1234, false, ""},
		{"1234", 0x1234, 0x1234, false, ""},
		{"$0000-$3FFF", 0x0000, 0x3FFF, false, ""},
		{"0x1000-0x2000", 0x1000, 0x2000, false, ""},
		{"$3FFF-$0000", 0, 0, true, "LO > HI"},
		{"NOTHEX", 0, 0, true, "bad LO"},
		{"$1234-NOTHEX", 0, 0, true, "bad HI"},
	}
	for _, c := range cases {
		lo, hi, err := parseFirstEntryRange(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseFirstEntryRange(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr {
			if lo != c.wantLo || hi != c.wantHi {
				t.Errorf("parseFirstEntryRange(%q) = ($%04X-$%04X), want ($%04X-$%04X)", c.in, lo, hi, c.wantLo, c.wantHi)
			}
		}
	}
}

func TestDirectoryExists(t *testing.T) {
	dir := t.TempDir()
	if !directoryExists(dir) {
		t.Errorf("directoryExists on real dir = false")
	}
	// File-not-dir.
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if directoryExists(f) {
		t.Errorf("directoryExists on file = true, want false")
	}
	if directoryExists("/nonexistent/path") {
		t.Errorf("directoryExists on missing = true")
	}
}

func TestFmtBank(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{-1, "any"},
		{-100, "any"},
		{0, "0"},
		{7, "7"},
		{111, "111"},
	}
	for _, c := range cases {
		got := fmtBank(c.in)
		if got != c.want {
			t.Errorf("fmtBank(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseOffsetRange(t *testing.T) {
	cases := []struct {
		in      string
		wantLo  uint16
		wantHi  uint16
		wantErr bool
	}{
		{"$1000", 0x1000, 0x1000, false},
		{"1000", 0x1000, 0x1000, false},
		{"$0000-$1FFF", 0x0000, 0x1FFF, false},
		// Mask to 13 bits ($1FFF) — value above is truncated.
		{"$2000", 0x0000, 0x0000, false},
		{"$3FFF", 0x1FFF, 0x1FFF, false},
		{"$1FFF-$0000", 0, 0, true}, // LO > HI
		{"NOTHEX", 0, 0, true},
		{"$1000-NOTHEX", 0, 0, true},
	}
	for _, c := range cases {
		lo, hi, err := parseOffsetRange(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseOffsetRange(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr {
			if lo != c.wantLo || hi != c.wantHi {
				t.Errorf("parseOffsetRange(%q) = ($%04X-$%04X), want ($%04X-$%04X)", c.in, lo, hi, c.wantLo, c.wantHi)
			}
		}
	}
}

func TestParseWatchSpecSkipsBadAddr(t *testing.T) {
	got := parseWatchSpec("a:1000,b:NOTHEX,c:2000")
	want := []watchEntry{
		{name: "a", addr: 0x1000},
		{name: "c", addr: 0x2000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWatchSpec skip-bad-addr = %+v, want %+v", got, want)
	}
}
