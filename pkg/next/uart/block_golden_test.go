package uart

import (
	"bufio"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestBlockMatchesFPGAGolden replays a golden captured from the real
// serial/uart.vhd (both UARTs, the Rx/Tx FIFOs, the status and error logic and
// the register map) simulated under GHDL, through Block. Tooling:
// _tools/uartblock-vhdl-test/.
//
// The golden is a time-ordered script. S lines are stimulus; O lines are the
// monitored output changes the real core produced, with the tick they
// happened on. The replay applies each S line, ticks the Block exactly as the
// testbench did, and compares every output change and every register read
// value against the golden. See tb_uartblock.vhd for the record formats.
func TestBlockMatchesFPGAGolden(t *testing.T) {
	replayBlockGolden(t, false)
}

// TestBlockAdvanceMatchesFPGAGolden replays the same golden with every idle
// span run through Block.Advance, which skips Clocks once the Block is
// quiescent. The golden holds every output change, so a skip that hid one
// would leave it unproduced, and one that let state drift would shift a later
// change or change a register read.
func TestBlockAdvanceMatchesFPGAGolden(t *testing.T) {
	replayBlockGolden(t, true)
}

func replayBlockGolden(t *testing.T, useAdvance bool) {
	f, err := os.Open("testdata/uartblock_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	b := NewBlock()
	var (
		cyc      int
		mon      bool
		prev     [16]int
		expected []string
		failed   int
	)
	for i := range prev {
		prev[i] = -1
	}
	fail := func(format string, a ...any) {
		failed++
		if failed <= 12 {
			t.Errorf("cycle %d: "+format, append([]any{cyc}, a...)...)
		}
	}
	pins := func() [16]int {
		bi := func(v bool) int {
			if v {
				return 1
			}
			return 0
		}
		return [16]int{
			bi(b.Tx(0)), bi(b.Tx(1)), bi(b.RxRTRn(0)), bi(b.RxRTRn(1)),
			bi(b.RxAvail(0)), bi(b.RxAvail(1)), bi(b.RxNearFull(0)), bi(b.RxNearFull(1)),
			bi(b.RxErr(0)), bi(b.RxErr(1)), bi(b.RxBreak(0)), bi(b.RxBreak(1)),
			bi(b.TxEmpty(0)), bi(b.TxEmpty(1)), bi(b.HWFlow(0)), bi(b.HWFlow(1)),
		}
	}
	monitor := func() {
		if !mon {
			return
		}
		cur := pins()
		for i := range cur {
			if cur[i] == prev[i] {
				continue
			}
			prev[i] = cur[i]
			got := fmt.Sprintf("O %d %d %d", cyc, i, cur[i])
			if len(expected) == 0 {
				fail("unexpected output change %q (golden has none pending)", got)
				continue
			}
			if want := expected[0]; want != got {
				fail("output change %q, golden %q", got, want)
			}
			expected = expected[1:]
		}
	}
	// checkCapture restores a capture of the Block into a fresh one and
	// requires them to be the same machine. The golden leaves something in
	// every part of the Block over its length, so a field the capture forgets
	// is caught at some point in it.
	captures, lastCapture := 0, -1000
	checkCapture := func() {
		if cyc-lastCapture < 29 { // thin them out: the states repeat
			return
		}
		lastCapture = cyc
		captures++
		c := NewBlock()
		if err := c.LoadState(b.SaveState()); err != nil {
			t.Fatalf("cycle %d: %v", cyc, err)
		}
		if d := firstDiff("block", reflect.ValueOf(b), reflect.ValueOf(c)); d != "" {
			t.Fatalf("cycle %d: restored Block differs: %s", cyc, d)
		}
	}
	// interesting is a state worth capturing: a one-cycle strobe is up, or a
	// FIFO pointer manager has a read or write in flight.
	interesting := func() bool {
		for u := 0; u < 2; u++ {
			if b.rx[u].rxAvail || b.rx[u].errFr || b.rx[u].errPar || b.rxAvailD[u] ||
				b.rxFifo[u].rdDly || b.rxFifo[u].wrDly || b.txFifo[u].rdDly || b.txFifo[u].wrDly ||
				b.tx[u].txEn || b.txRdD[u] || b.txWrD[u] {
				return true
			}
		}
		return false
	}
	tick := func() {
		b.Clock()
		cyc++
		monitor()
		if useAdvance && (interesting() || cyc%509 == 0) {
			checkCapture()
		}
	}
	ticks := func(n int) {
		for i := 0; i < n; i++ {
			tick()
		}
	}
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("bad number %q", s)
		}
		return n
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	nStim := 0
	for sc.Scan() {
		fld := strings.Fields(sc.Text())
		if len(fld) == 0 {
			continue
		}
		if fld[0] == "O" {
			expected = append(expected, sc.Text())
			continue
		}
		if fld[0] != "S" {
			t.Fatalf("unknown record %q", sc.Text())
		}
		nStim++
		switch fld[1] {
		case "RST":
			b.SetReset(true)
			ticks(atoi(fld[2]))
			b.SetReset(false)
			mon = true
		case "IDLE":
			if useAdvance {
				// In chunks, so captures land inside the span.
				for left := atoi(fld[2]); left > 0; {
					n := min(left, 211)
					base := cyc
					b.Advance(n, func(i int) {
						cyc = base + i + 1
						monitor()
						if interesting() {
							checkCapture()
						}
					})
					cyc = base + n
					left -= n
				}
			} else {
				ticks(atoi(fld[2]))
			}
		case "WR":
			b.SetIO(byte(atoi(fld[2])), false, true, byte(atoi(fld[3])))
			ticks(atoi(fld[4]))
			b.SetIO(byte(atoi(fld[2])), false, false, byte(atoi(fld[3])))
		case "RD":
			b.SetIO(byte(atoi(fld[2])), true, false, 0)
			ticks(atoi(fld[3]))
			if got, want := int(b.CPUData()), atoi(fld[4]); got != want {
				fail("S RD reg %s read $%02X, golden $%02X", fld[2], got, want)
			}
			b.SetIO(byte(atoi(fld[2])), false, false, 0)
		case "RX":
			b.SetRx(atoi(fld[2]), atoi(fld[3]) != 0)
		case "CTS":
			b.SetCTSn(atoi(fld[2]), atoi(fld[3]) != 0)
		case "LOOP":
			b.SetLoopback(atoi(fld[2]), atoi(fld[3]) != 0)
		default:
			t.Fatalf("unknown stimulus %q", sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 0 {
		fail("%d golden output changes never produced, first %q", len(expected), expected[0])
	}
	if useAdvance {
		t.Logf("%d captures restored", captures)
	}
	if failed > 0 {
		t.Fatalf("%d divergences from the FPGA golden over %d stimulus records", failed, nStim)
	}
}
