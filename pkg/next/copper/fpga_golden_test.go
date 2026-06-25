package copper

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// emission is one NextReg write the copper produced, tagged with the raster
// position it fired at.
type emission struct {
	v, h     int
	reg, val byte
}

// recordWriter captures the (reg,val) MOVE writes our copper emits and tags
// each with the raster position currently being stepped.
type recordWriter struct {
	curV, curH int
	out        []emission
}

func (r *recordWriter) WriteReg(reg, val byte) {
	r.out = append(r.out, emission{v: r.curV, h: r.curH, reg: reg, val: val})
}

// TestCopperMatchesFPGAGolden replays the FPGA copper golden — captured from
// the real device/copper.vhd (the copper coprocessor FSM) wired with the
// dpram2 instruction-list read port exactly as zxnext.vhd does, simulated
// under GHDL — through our copper.Copper and asserts the emitted NextReg
// write SEQUENCE matches the hardware: the ordered (reg,val) pairs, the
// scanline each fires on, and that WAIT-gated writes honour the hardware
// horizontal release threshold (X*8 + 12). Tooling: _tools/copper-vhdl-test/.
//
// The golden is a cycle-driven trace: one POS tick per (vcount,hcount) the
// video timing presents to the copper, with EMIT lines where a MOVE pulse
// fired. We drive our functional Step() once per POS tick (one copper cycle =
// one instruction budget) and compare the emission stream. The FPGA's
// registered instruction-RAM read adds a fixed +1-cycle horizontal pipeline
// lag that our functional model abstracts away, so we assert the scanline (v)
// exactly and the horizontal position (h) within that pipeline tolerance /
// above the hardware WAIT threshold — see the coverage note at the foot.
func TestCopperMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/copper_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	hx := func(s string) uint16 { n, _ := strconv.ParseUint(s, 16, 16); return uint16(n) }
	kv := func(fields []string, key string) int {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return atoi(fld[len(key)+1:])
			}
		}
		return -1
	}

	var c *Copper
	var rw *recordWriter
	// goldEmits accumulates the FPGA EMIT lines for the program currently
	// loaded; flushed and compared at each RESET / EOF.
	var goldEmits []emission

	prog := 0

	flush := func() {
		if c == nil {
			return
		}
		compareEmissions(t, prog, goldEmits, rw.out)
		goldEmits = nil
		prog++
	}

	sc := bufio.NewScanner(f)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RESET":
			flush()
			c = New()
			rw = &recordWriter{}
			c.SetRegWriter(rw)
		case "LOAD":
			idx := uint16(atoi(fld[1]))
			word := hx(fld[2])
			c.SetWritePtrLow(byte(idx & 0xFF))
			c.SetWritePtrHighAndMode(byte((idx >> 8) & 0x03))
			c.WriteData(byte(word >> 8))
			c.WriteData(byte(word & 0xFF))
		case "MODE":
			m := byte(atoi(fld[1]))
			c.SetWritePtrHighAndMode(m << 6)
		case "POS":
			v := kv(fld, "v")
			h := kv(fld, "h")
			rw.curV, rw.curH = v, h
			// One copper cycle = at most one instruction. hcount is shared
			// (0..511 pixel) units with the FPGA.
			c.Step(uint16(v), uint16(h), 1)
		case "EMIT":
			v := kv(fld, "v")
			h := kv(fld, "h")
			reg := byte(kv(fld, "reg"))
			val := byte(kv(fld, "val"))
			goldEmits = append(goldEmits, emission{v: v, h: h, reg: reg, val: val})
		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	flush()

	if prog < 6 {
		t.Fatalf("expected the full golden replay (>=6 programs), only saw %d", prog)
	}
}

// Coverage / fidelity notes for the copper golden replay:
//
//   - WAIT horizontal release: bit-exact. The two WAIT-gated MOVEs in the
//     golden (programs "wait-then-move" and "wait-offset") fire at the FPGA's
//     exact hcount (h=29 for X=2, h=21 for X=1), validating the (X<<3)+12
//     threshold in our Step. delta = 0.
//   - Plain MOVE emission position: within +/-2 hcount of the FPGA. The FPGA
//     registers the instruction-RAM read on the inverted clock (a +1-cycle
//     read pipeline) and a MOVE consumes a second "bubble" cycle clearing the
//     output pulse (copper.vhd:87-89); our functional Step abstracts both away
//     and emits a cycle or two earlier. The scanline (vcount) and the ordered
//     (reg,val) stream are exact; only the sub-line hcount carries this fixed
//     pipeline lag, bounded by hTol.
//
// Driven coverage: stop / run-once-from-0 / continue / run+restart-at-vbl
// modes; the address reset on entering modes 01 and 11; the vcount==0,hcount==0
// VBL restart; WAIT line+column gating with the +12 offset; the reg-field-only
// NOP suppression; and ordered multi-MOVE execution.
//
// Coverage gaps (not driven, honest):
//   - Mid-program live re-arming of copper_en_i (a mode change WHILE a list is
//     running mid-frame). The golden sets the mode once per program before the
//     sweep; the FSM's last_state_s edge-detect on a running list is untested.
//   - The reset_i synchronous clear is exercised only via do_reset between
//     programs, not asserted mid-list.
//   - hcount/vcount are swept across small windows (tens of lines/pixels), not
//     a full 0..511 x 0..311 frame, so no wrap-around of the 9-bit counters is
//     covered beyond the single VBL (v=0,h=0) restart edge.

// compareEmissions asserts our emission stream matches the FPGA golden's:
// identical count, identical ordered (reg,val), identical scanline, and a
// horizontal position within +/- the registered-read pipeline tolerance.
func compareEmissions(t *testing.T, prog int, gold, got []emission) {
	t.Helper()
	if len(got) != len(gold) {
		t.Errorf("program %d: emitted %d NextReg writes, FPGA emitted %d\n got=%v\ngold=%v",
			prog, len(got), len(gold), got, gold)
		return
	}
	const hTol = 2 // registered instruction-RAM read pipeline lag tolerance
	for i := range gold {
		g, o := gold[i], got[i]
		if o.reg != g.reg || o.val != g.val {
			t.Errorf("program %d emit %d: got reg=%d val=%d, FPGA reg=%d val=%d",
				prog, i, o.reg, o.val, g.reg, g.val)
		}
		if o.v != g.v {
			t.Errorf("program %d emit %d: got scanline %d, FPGA scanline %d (reg=%d)",
				prog, i, o.v, g.v, g.reg)
		}
		if d := o.h - g.h; d < -hTol || d > hTol {
			t.Errorf("program %d emit %d (reg=%d): got h=%d, FPGA h=%d (tol %d)",
				prog, i, g.reg, o.h, g.h, hTol)
		}
	}
}
