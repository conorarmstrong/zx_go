package compositor

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestMixerMatchesFPGAGolden replays the FPGA video-mixer golden — captured by
// extracting the stage-2 layer/SLU-priority block of zxnext.vhd (lines
// 7100-7355) verbatim into a standalone entity and simulating it under GHDL
// (tooling: _tools/mixer-vhdl-test/) — through Mix() and asserts the final
// 9-bit RGB333 output matches the hardware for all 8 NR$15 layer orderings, the
// SLU stencil, and the two additive blend modes.
func TestMixerMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/mixer_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	hx := func(s string) uint16 { v, _ := strconv.ParseUint(s, 16, 16); return uint16(v) }
	b := func(s string) bool { return s == "1" }

	sc := bufio.NewScanner(f)
	ln, n := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// V pri blend st bo trans fb ula uen uc tm ten tt tmen tb spr sen l2 l2en l2p = out
		fd := strings.Fields(line)
		if len(fd) != 22 || fd[0] != "V" || fd[20] != "=" {
			t.Fatalf("line %d: malformed golden row: %q", ln, line)
		}
		in := MixerInputs{
			Priority:       byte(atoi(fd[1])),
			BlendMode:      byte(atoi(fd[2])),
			StencilMode:    b(fd[3]),
			Border:         b(fd[4]),
			Transparent:    byte(hx(fd[5])),
			Fallback:       byte(hx(fd[6])),
			ULARGB:         hx(fd[7]),
			ULAEn:          b(fd[8]),
			ULAClipped:     b(fd[9]),
			TMRGB:          hx(fd[10]),
			TMPixelEn:      b(fd[11]),
			TMTextmode:     b(fd[12]),
			TMEn:           b(fd[13]),
			TMBelow:        b(fd[14]),
			SpriteRGB:      hx(fd[15]),
			SpriteEn:       b(fd[16]),
			Layer2RGB:      hx(fd[17]),
			Layer2En:       b(fd[18]),
			Layer2Priority: b(fd[19]),
		}
		want := hx(fd[21])
		if got := Mix(in); got != want {
			t.Errorf("line %d: Mix mismatch: got %03X want %03X\n  inputs: %q", ln, got, want, line)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 140 {
		t.Fatalf("expected the full mixer golden replay, only saw %d vectors", n)
	}
}
