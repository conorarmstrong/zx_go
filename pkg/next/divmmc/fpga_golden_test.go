package divmmc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestDivMMCMatchesFPGAGolden replays the FPGA divMMC golden — captured from
// the real device/divmmc.vhd (the automap-hold FSM + paging decode) wrapped
// with the zxnext.vhd entry-point trigger decode lifted verbatim, simulated
// under GHDL — through the Pager and asserts the per-fetch paging decision
// matches the hardware exactly. Tooling: _tools/divmmc-vhdl-test/.
//
// The golden samples the divMMC outputs DURING each M1 fetch (phase B):
// o_divmmc_rom_en / o_divmmc_ram_en / o_divmmc_rdonly / o_divmmc_ram_bank /
// o_disable_nmi. Those are reproduced by Step() (apply this fetch's page-in)
// then DecodePaging()/DisableNMI(); PostStep() then applies the end-of-M1
// page-out so it affects the NEXT fetch. The automap_held latch (which our
// model collapses into pagedIn) is verified indirectly via the page0 overlay
// outputs on subsequent fetches.
func TestDivMMCMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/divmmc_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var p *Pager
	rom3Active := false

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	kv := func(fields []string, key string) int {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return atoi(fld[len(key)+1:])
			}
		}
		return -1
	}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}

	sc := bufio.NewScanner(f)
	ln, fetches := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RESET":
			p = New(nil)
			p.SetAutomap(false)
			p.SetEntryPoints0(0)
			p.SetEntryPoints1(0)
			p.SetEntryPointsValid0(0)
			p.SetEntryPointsTiming0(0)
			rom3Active = false
			p.SetRom3Query(func(uint16) bool { return rom3Active })
		case "E3":
			p.WritePort(0x00E3, byte(atoi(fld[1])))
		case "B8":
			p.SetEntryPoints0(byte(atoi(fld[1])))
		case "B9":
			p.SetEntryPointsValid0(byte(atoi(fld[1])))
		case "BA":
			p.SetEntryPointsTiming0(byte(atoi(fld[1])))
		case "BB":
			p.SetEntryPoints1(byte(atoi(fld[1])))
		case "EN":
			p.SetEnabled(atoi(fld[1]) != 0)
		case "ACTIVE":
			p.SetAutomap(atoi(fld[1]) != 0)
		case "ROM3":
			rom3Active = atoi(fld[1]) != 0
		case "BUTTON":
			p.AssertNMIButton()
		case "RETN":
			p.HandleRETN()
		case "F":
			addr64, _ := strconv.ParseUint(fld[1], 16, 16)
			addr := uint16(addr64)
			wantRom, wantRam := kv(fld, "rom"), kv(fld, "ram")
			wantRd, wantBank, wantNmi := kv(fld, "rd"), kv(fld, "bank"), kv(fld, "nmi")

			p.Step(addr)
			d := p.DecodePaging(addr)
			gotNmi := b2i(p.DisableNMI())
			p.PostStep(addr)

			if b2i(d.ROMEn) != wantRom || b2i(d.RAMEn) != wantRam ||
				b2i(d.RDOnly) != wantRd || d.Bank != wantBank || gotNmi != wantNmi {
				t.Errorf("line %d fetch %04X: got rom=%d ram=%d rd=%d bank=%d nmi=%d; want rom=%d ram=%d rd=%d bank=%d nmi=%d",
					ln, addr, b2i(d.ROMEn), b2i(d.RAMEn), b2i(d.RDOnly), d.Bank, gotNmi,
					wantRom, wantRam, wantRd, wantBank, wantNmi)
			}
			fetches++
		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if fetches < 25 {
		t.Fatalf("expected the full golden replay, only saw %d fetches", fetches)
	}
}
