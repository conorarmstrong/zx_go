package keyboard

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestKeyboardMatchesFPGAGolden replays the keyboard golden — captured from the
// real FPGA membrane scanner (cores/zxnext/src/input/membrane/membrane.vhd, the
// 8x7 matrix scan + the standard-5-bit o_cols read) simulated under GHDL —
// through our Keyboard and asserts the per-row $FE read matches the hardware.
// Tooling: _tools/keyboard-vhdl-test/.
//
// Each golden record is one of:
//
//	PRESS r=<row> c=<col>     a physical key held for the current sequence; col
//	                          is a STANDARD-matrix column 0..4, which maps
//	                          directly to our matrix bit (1 << col).
//	RELEASE                   all keys lifted.
//	R rows=<hex2> cols=<hex2> the membrane's i_rows select (= the CPU A15:A8
//	                          keyboard-read select, active-low) and the resulting
//	                          o_cols (= the D4:D0 the CPU reads on port $FE).
//
// We drive PRESS via PressMatrixKey (the raw matrix accessor, bypassing host-key
// mapping) and reproduce each R read via Scan(addr) with addr's high byte set to
// the i_rows pattern. The FPGA is the oracle: any mismatch is our bug.
//
// Coverage note: the membrane's physical columns 5..6 carry the EXTENDED keys
// (arrows, EDIT, DELETE, GRAPH, CAPS/TRUE/INV VIDEO, BREAK, ; " , .) which the
// hardware folds into composite CAPS/SYM presses (membrane.vhd 232-240). Our
// model expresses those at the host-key-mapping layer (initKeyMap composites),
// not inside the matrix scan, so this golden exercises only the standard 8x5
// matrix (columns 0..4); the extended-column fold-in is untested here.
func TestKeyboardMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/keyboard_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	kbd := New()
	kbd.ReleaseAll()

	hex2 := func(s string) byte {
		v, err := strconv.ParseUint(s, 16, 16)
		if err != nil {
			t.Fatalf("bad hex %q: %v", s, err)
		}
		return byte(v)
	}
	field := func(fields []string, key string) string {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return fld[len(key)+1:]
			}
		}
		return ""
	}

	sc := bufio.NewScanner(f)
	ln, reads := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RELEASE":
			kbd.ReleaseAll()
		case "PRESS":
			row, _ := strconv.Atoi(field(fld, "r"))
			col, _ := strconv.Atoi(field(fld, "c"))
			kbd.PressMatrixKey(row, byte(1)<<uint(col), true)
		case "R":
			rows := hex2(field(fld, "rows"))
			wantCols := hex2(field(fld, "cols")) & 0x1F
			got := kbd.Scan(uint16(rows)<<8) & 0x1F
			if got != wantCols {
				t.Errorf("line %d rows=%02X: got cols=%02X want %02X", ln, rows, got, wantCols)
			}
			reads++
		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if reads < 40 {
		t.Fatalf("expected the full golden replay, only saw %d reads", reads)
	}
}
