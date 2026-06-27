package next

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestIM2DaisyChainMatchesFPGAGolden replays the FPGA IM2 daisy-chain golden —
// captured from the real device/peripherals.vhd + im2_device.vhd +
// im2_peripheral.vhd under GHDL (tooling: _tools/im2-vhdl-test/) — through
// IM2DaisyChain.Tick and asserts /INT, the vector, IEO and the sticky status
// match the hardware across single/simultaneous/nested-interrupt + EOI-unwind
// scenarios. The testbench resets once at the start, so the replay ticks
// straight through.
func TestIM2DaisyChainMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/im2_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	field := func(fields []string, key string) string {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return fld[len(key)+1:]
			}
		}
		return ""
	}
	hexv := func(s string) uint64 { v, _ := strconv.ParseUint(s, 16, 64); return v }
	bit := func(s string, i int) bool { return hexv(s)&(1<<uint(i)) != 0 }

	c := NewIM2DaisyChain()
	sc := bufio.NewScanner(f)
	ln, n := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		halves := strings.SplitN(line, "::", 2)
		if len(halves) != 2 {
			t.Fatalf("line %d: malformed golden row: %q", ln, line)
		}
		inF := strings.Fields(halves[0])  // T req=.. en=.. ...
		outF := strings.Fields(halves[1]) // int_n=.. vec=.. ieo=.. status=..

		reqH, enH, unqH, clrH := field(inF, "req"), field(inF, "en"), field(inF, "unq"), field(inF, "clr")
		var in IM2Inputs
		for i := 0; i < IM2NumPeriph; i++ {
			in.IntReq[i] = bit(reqH, i)
			in.IntEn[i] = bit(enH, i)
			in.IntUnq[i] = bit(unqH, i)
			in.StatusClear[i] = bit(clrH, i)
		}
		in.HWIM2 = field(inF, "hwim2") == "1"
		in.M1 = field(inF, "m1") == "1"
		in.IORQ = field(inF, "iorq") == "1"
		in.RetiDecode = field(inF, "reti_d") == "1"
		in.RetiSeen = field(inF, "reti_s") == "1"
		in.IM2 = field(inF, "im2") == "1"

		c.Tick(in)

		wantIntN := field(outF, "int_n") == "1"
		wantVec, _ := strconv.Atoi(field(outF, "vec"))
		wantIEO := field(outF, "ieo") == "1"
		wantStatus := hexv(field(outF, "status"))

		if c.IntN() != wantIntN || c.Vector() != wantVec || c.IEO() != wantIEO || uint64(c.StatusBits()) != wantStatus {
			t.Errorf("line %d: got int_n=%v vec=%d ieo=%v status=%04X; want int_n=%v vec=%d ieo=%v status=%04X\n  %q",
				ln, c.IntN(), c.Vector(), c.IEO(), c.StatusBits(),
				wantIntN, wantVec, wantIEO, wantStatus, line)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 50 {
		t.Fatalf("expected the full IM2 golden replay, only saw %d ticks", n)
	}
}
