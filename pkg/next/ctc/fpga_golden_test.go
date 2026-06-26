package ctc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestCTCMatchesFPGAGolden replays the FPGA Z80 CTC golden — captured from the
// real device/ctc_chan.vhd (one counter/timer channel: control-word decode,
// the 16/256 prescaler, timer vs counter mode, the down-counter with
// time-constant reload, and the registered zero-count ZC/TO pulse) simulated
// under GHDL — through this package's Channel and asserts the live down-counter
// and the ZC/TO output match the hardware cycle for cycle.
// Tooling: _tools/ctc-vhdl-test/.
//
// Golden op lines:
//
//	; <label>            sequence header (ignored)
//	RESET                hard-reset pulse           -> HardReset()
//	W <hex>              CPU byte write             -> Write(b)
//	IE <0|1>             separate int-enable write  -> WriteIntEnable(v)
//	TRG <0|1>            set external trigger level -> SetTrigger(v)
//	C count=<n> zc=<0|1> one i_CLK cycle            -> Tick(); assert Count()/ZCTO()
//
// The W/IE/TRG ops mirror exactly how the testbench drives the channel, so the
// only assertions are on the C lines (the live count and the registered ZC/TO).
func TestCTCMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/ctc_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	kvInt := func(fields []string, key string) (int, bool) {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return atoi(fld[len(key)+1:]), true
			}
		}
		return 0, false
	}

	var ch *Channel
	label := ""
	cycles := 0

	sc := bufio.NewScanner(f)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, ";") {
			label = strings.TrimSpace(line[1:])
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RESET":
			if ch == nil {
				ch = New()
			} else {
				ch.HardReset()
			}
		case "W":
			b, err := strconv.ParseUint(fld[1], 16, 8)
			if err != nil {
				t.Fatalf("line %d: bad write byte %q: %v", ln, fld[1], err)
			}
			ch.Write(uint8(b))
		case "IE":
			ch.WriteIntEnable(fld[1] == "1")
		case "TRG":
			ch.SetTrigger(fld[1] == "1")
		case "C":
			want, ok := kvInt(fld, "count")
			if !ok {
				t.Fatalf("line %d: C line missing count: %q", ln, line)
			}
			zc, _ := kvInt(fld, "zc")
			ch.Tick()
			cycles++
			if got := int(ch.Count()); got != want {
				t.Fatalf("line %d [%s]: count = %d, want %d (FPGA)", ln, label, got, want)
			}
			gotZC := 0
			if ch.ZCTO() {
				gotZC = 1
			}
			if gotZC != zc {
				t.Fatalf("line %d [%s]: zc = %d, want %d (FPGA)", ln, label, gotZC, zc)
			}
		default:
			t.Fatalf("line %d: unknown op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if cycles == 0 {
		t.Fatal("no cycles replayed — golden empty?")
	}
	t.Logf("replayed %d CTC clock cycles against the FPGA golden", cycles)
}
