package palette

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestPaletteMatchesFPGAGolden replays the FPGA palette-write golden — the
// value/priority decode (zxnext.vhd 4918-4920) + the NR$40/$41/$43/$44 index
// FSM (5374-5402) extracted verbatim and simulated under GHDL (tooling:
// _tools/palette-vhdl-test/) — through the Bank and asserts the 8->9-bit value
// expansion, the NR$44 two-step + priority write, and the index
// auto-increment (including the NR$43 bit7 auto-increment-DISABLE) all match.
func TestPaletteMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/palette_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// FPGA write_select (NR$43 bits 6:4) -> our Zeus palette-slot order
	// (wire.go's wselToZeus).
	wselToZeus := [8]byte{
		PaletteULAFirst, PaletteLayer2First, PaletteSpritesFirst, PaletteTilemapFirst,
		PaletteULASecond, PaletteLayer2Second, PaletteSpritesSecond, PaletteTilemapSecond,
	}

	field := func(fl []string, k string) string {
		for _, x := range fl {
			if strings.HasPrefix(x, k+"=") {
				return x[len(k)+1:]
			}
		}
		return ""
	}
	h := func(s string) int { v, _ := strconv.ParseUint(s, 16, 32); return int(v) }

	b := NewBank()
	sc := bufio.NewScanner(f)
	ln, n := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fl := strings.Fields(line)
		reg, dat := h(field(fl, "reg")), h(field(fl, "dat"))

		switch reg {
		case 0x40:
			b.SetIndex(byte(dat))
		case 0x41:
			b.Write8(byte(dat))
		case 0x43:
			b.Select(wselToZeus[(dat>>4)&0x07])
			b.SetAutoIncDisable(dat&0x80 != 0)
		case 0x44:
			b.WriteNR44(byte(dat))
		default:
			t.Fatalf("line %d: unexpected reg %02X", ln, reg)
		}

		if field(fl, "we") == "1" {
			zeus := wselToZeus[h(field(fl, "tgt"))]
			idx := byte(h(field(fl, "idx")))
			wantVal := uint16(h(field(fl, "val")))
			wantPrio := byte(h(field(fl, "prio")))
			pal := b.Palette(int(zeus))
			if got := pal.Get(idx); got != wantVal {
				t.Errorf("line %d: palette[%d][%02X] value = %03X, want %03X", ln, zeus, idx, got, wantVal)
			}
			if got := pal.Priority(idx); got != wantPrio {
				t.Errorf("line %d: palette[%d][%02X] priority = %d, want %d", ln, zeus, idx, got, wantPrio)
			}
		}
		if wantIdx := byte(h(field(fl, "nextidx"))); b.Index() != wantIdx {
			t.Errorf("line %d (reg %02X dat %02X): index = %02X, want %02X", ln, reg, dat, b.Index(), wantIdx)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 18 {
		t.Fatalf("expected the full palette golden replay, only saw %d ops", n)
	}
}
