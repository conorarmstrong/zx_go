package memory

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestPagingMatchesFPGAGolden replays paging/MMU operation sequences whose
// expected address->physical-page mappings were captured from the actual
// Spectrum Next FPGA memory controller (the MMU bank-decode extracted from
// zxnext.vhd and simulated under GHDL) and asserts ours' bank decode produces
// the identical mapping. This is the paging analogue of the CPU/sprite golden
// suites: the golden IS the hardware's behaviour, so a green run proves our
// $7FFD/$1FFD/$DFFD/NextReg-MMU paging is faithful to the FPGA — the subsystem
// that has caused the most bugs (128K launch, divMMC, NBI 590 class).
//
// Golden format (testdata/paging_golden.txt), one token per line:
//
//	RESET                  start a fresh machine (paging at soft-reset defaults)
//	W7FFD <v> / W1FFD <v>  classic 128K / +3 paging-port write
//	WDFFD <v>              Next high-bank extension port write
//	MMU <slot> <page>      NextReg $50+slot = 8K page
//	W8E <v>                NextReg $8E (128K memory-mapping) write
//	MAP <addrHex> <page>   assert: a CPU read at addr maps to 8K page <page>
//	                       (page = ROM for a ROM-mapped slot)
//
// Self-contained — no GHDL at run time. Skips if the golden is not present
// (generated locally by _tools/paging-vhdl-test/).
func TestPagingMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/paging_golden.txt")
	if err != nil {
		t.Skip("no paging golden (generate via _tools/paging-vhdl-test/)")
	}
	defer func() { _ = f.Close() }()

	newMem := func() *Memory {
		m, err := New(mmuTestROMs(t), roms.ModelNext)
		if err != nil {
			t.Fatal(err)
		}
		m.PagingEnabled = true
		return m
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	hx := func(s string) uint16 { v, _ := strconv.ParseUint(strings.TrimPrefix(s, "$"), 16, 32); return uint16(v) }

	m := newMem()
	sc := bufio.NewScanner(f)
	n, fails := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		switch p[0] {
		case "RESET":
			m = newMem()
		case "W7FFD":
			m.PageMemory(byte(atoi(p[1])))
		case "W1FFD":
			m.PageMemoryPlus3(byte(atoi(p[1])))
		case "WDFFD":
			m.SetDFFD(byte(atoi(p[1])))
		case "MMU":
			m.SetMMU(byte(atoi(p[1])), byte(atoi(p[2])))
		case "W8E":
			m.SetROMBankExtended(byte(atoi(p[1])))
		case "MAP":
			addr := hx(p[1])
			want := -1
			if p[2] != "ROM" {
				want = atoi(p[2])
			}
			got := m.ResolvePage(addr)
			if got != want {
				if fails < 25 {
					t.Errorf("MAP $%04X: got page %d, want %d (FPGA golden)", addr, got, want)
				}
				fails++
			}
			n++
		default:
			t.Fatalf("unknown golden op: %q", line)
		}
	}
	if fails > 0 {
		t.Fatalf("%d/%d address mappings diverged from the FPGA", fails, n)
	}
	t.Logf("replayed %d FPGA paging mappings — all match the hardware MMU", n)
}
