package uart

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestUARTMatchesFPGAGolden replays the FPGA UART golden — captured from the
// real serial/uart_tx.vhd, serial/uart_rx.vhd and serial/fifop.vhd (the latter
// pulling in misc/debounce.vhd) simulated under GHDL — through our cycle-level
// Go models (TX / RX / FIFOP) and asserts the per-clock serial bit stream, the
// recovered RX bytes + error strobes, and the FIFO status-flag geometry match
// the hardware exactly. Tooling: _tools/uart-vhdl-test/.
//
// The golden records:
//
//	TX byte=.. presc=.. frame=.. n=N  +  TXBITS <N chars>  (o_Tx per clock)
//	RX byte=.. presc=.. frame=..      +  RXRESULT avail/byte/framing/parity
//	FIFO/FIFO2 <tag> empty/near/almost/full/waddr/raddr
func TestUARTMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/uart_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	// kvInt reads "key=val" (decimal) from a fields slice.
	kvInt := func(fld []string, key string) int {
		for _, x := range fld {
			if strings.HasPrefix(x, key+"=") {
				return atoi(x[len(key)+1:])
			}
		}
		return -1
	}
	kvStr := func(fld []string, key string) string {
		for _, x := range fld {
			if strings.HasPrefix(x, key+"=") {
				return x[len(key)+1:]
			}
		}
		return ""
	}

	// FIFO instances keyed by record tag prefix.
	fifo3 := NewFIFOP(3) // "FIFO "  records (depth 3)
	fifo4 := NewFIFOP(4) // "FIFO2 " records (depth 4)
	// pulseFIFO reproduces the testbench f_pulse_wr / f_pulse_rd:
	//   set line=1, tick; set line=0, tick; tick;  (then sample)
	pulseFIFO := func(fp *FIFOP, write bool) {
		if write {
			fp.SetWrite(true)
			fp.Clock()
			fp.SetWrite(false)
			fp.Clock()
			fp.Clock()
		} else {
			fp.SetRead(true)
			fp.Clock()
			fp.SetRead(false)
			fp.Clock()
			fp.Clock()
		}
	}
	checkFIFO := func(fp *FIFOP, fld []string, ln int) {
		b2i := func(b bool) int {
			if b {
				return 1
			}
			return 0
		}
		gotEmpty := b2i(fp.Empty())
		gotNear := b2i(fp.NearFull())
		gotAlmost := b2i(fp.AlmostFull())
		gotFull := b2i(fp.Full())
		gotW := int(fp.WriteAddr())
		gotR := int(fp.ReadAddr())
		if gotEmpty != kvInt(fld, "empty") || gotNear != kvInt(fld, "near") ||
			gotAlmost != kvInt(fld, "almost") || gotFull != kvInt(fld, "full") ||
			gotW != kvInt(fld, "waddr") || gotR != kvInt(fld, "raddr") {
			t.Errorf("line %d %s %s: got empty=%d near=%d almost=%d full=%d waddr=%d raddr=%d; want empty=%d near=%d almost=%d full=%d waddr=%d raddr=%d",
				ln, fld[0], fld[1],
				gotEmpty, gotNear, gotAlmost, gotFull, gotW, gotR,
				kvInt(fld, "empty"), kvInt(fld, "near"), kvInt(fld, "almost"), kvInt(fld, "full"), kvInt(fld, "waddr"), kvInt(fld, "raddr"))
		}
	}

	sc := bufio.NewScanner(f)
	ln := 0
	txCount, rxCount, fifoCount := 0, 0, 0
	var pendingTX []string // the "TX ..." header awaiting its TXBITS line

	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "TX":
			pendingTX = fld

		case "TXBITS":
			if pendingTX == nil {
				t.Fatalf("line %d: TXBITS without preceding TX header", ln)
			}
			b := byte(kvInt(pendingTX, "byte"))
			presc := uint32(kvInt(pendingTX, "presc"))
			frame := byte(kvInt(pendingTX, "frame"))
			want := fld[1]
			n := len(want)

			tx := NewTX()
			tx.SetFrame(frame)
			tx.SetPrescaler(presc)
			// testbench tx_reset_seq holds reset across two ticks then
			// releases; our NewTX is already idle. The byte is then sent.
			tx.Send(b)
			_ = tx.Clock() // the en-strobe latch tick (testbench: tx_en<=1; tick)
			var got strings.Builder
			for i := 0; i < n; i++ {
				if tx.Clock() {
					got.WriteByte('1')
				} else {
					got.WriteByte('0')
				}
			}
			if got.String() != want {
				t.Errorf("line %d TX byte=%d presc=%d frame=%d:\n got %s\nwant %s",
					ln, b, presc, frame, got.String(), want)
			}
			pendingTX = nil
			txCount++

		case "RX":
			pendingTX = nil // (defensive)
			b := byte(kvInt(fld, "byte"))
			presc := kvInt(fld, "presc")
			frame := byte(kvInt(fld, "frame"))
			forceBadStop := false
			parBit := false
			// The next line is RXRESULT; read it now.
			if !sc.Scan() {
				t.Fatalf("line %d: RX header with no RXRESULT", ln)
			}
			ln++
			res := strings.Fields(strings.TrimSpace(sc.Text()))
			if len(res) == 0 || res[0] != "RXRESULT" {
				t.Fatalf("line %d: expected RXRESULT, got %q", ln, sc.Text())
			}
			wantAvail := kvInt(res, "avail")
			wantByte := kvStr(res, "byte")
			wantFr := kvInt(res, "framing")
			wantPar := kvInt(res, "parity")
			// A framing-error case is identifiable: avail=0 && framing=1.
			if wantAvail == 0 && wantFr == 1 {
				forceBadStop = true
			}

			rx := NewRX()
			rx.SetFrame(frame)
			rx.SetPrescaler(uint32(presc))
			// settle idle high (testbench rx_reset_seq idles presc*4 clocks)
			for i := 0; i < presc*4; i++ {
				rx.Clock(true)
			}

			gotAvail, gotFr, gotPar := false, false, false
			var gotByte byte
			parityEn := (frame>>2)&1 == 1

			hold := func(level bool, cycles int) {
				for i := 0; i < cycles; i++ {
					rx.Clock(level)
					if rx.Avail() {
						gotAvail = true
						gotByte = rx.Byte()
					}
					if rx.FramingError() {
						gotFr = true
					}
					if rx.ParityError() {
						gotPar = true
					}
				}
			}

			hold(false, presc) // start bit
			for i := 0; i < 8; i++ {
				hold(b&(1<<i) != 0, presc) // 8 data bits LSB first
			}
			if parityEn {
				hold(parBit, presc)
			}
			if forceBadStop {
				hold(false, presc) // bad stop
			} else {
				hold(true, presc) // good stop
			}
			hold(true, presc*2) // trailing idle: observe strobe

			b2i := func(x bool) int {
				if x {
					return 1
				}
				return 0
			}
			gotByteHex := strings.ToUpper(strconv.FormatInt(int64(gotByte), 16))
			if len(gotByteHex) == 1 {
				gotByteHex = "0" + gotByteHex
			}
			if b2i(gotAvail) != wantAvail || (wantAvail == 1 && gotByteHex != wantByte) ||
				b2i(gotFr) != wantFr || b2i(gotPar) != wantPar {
				t.Errorf("line %d RX byte=%d presc=%d frame=%d: got avail=%d byte=%s framing=%d parity=%d; want avail=%d byte=%s framing=%d parity=%d",
					ln, b, presc, frame,
					b2i(gotAvail), gotByteHex, b2i(gotFr), b2i(gotPar),
					wantAvail, wantByte, wantFr, wantPar)
			}
			rxCount++

		case "FIFO", "FIFO2":
			fp := fifo3
			if fld[0] == "FIFO2" {
				fp = fifo4
			}
			tag := fld[1]
			switch {
			case tag == "init":
				// the golden's init line is after reset; both NewFIFOP and a
				// fresh reset start empty. Nothing to drive.
			case strings.HasPrefix(tag, "w"):
				pulseFIFO(fp, true)
			case strings.HasPrefix(tag, "r"):
				pulseFIFO(fp, false)
			default:
				t.Fatalf("line %d: unknown FIFO tag %q", ln, tag)
			}
			checkFIFO(fp, fld, ln)
			fifoCount++

		case "RXRESULT":
			// consumed inline with its RX header
			t.Fatalf("line %d: stray RXRESULT", ln)

		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if txCount < 5 || rxCount < 4 || fifoCount < 20 {
		t.Fatalf("incomplete golden replay: tx=%d rx=%d fifo=%d", txCount, rxCount, fifoCount)
	}
}
