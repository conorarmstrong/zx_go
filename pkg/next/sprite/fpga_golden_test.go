package sprite

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// loadSpriteGolden reads the FPGA-derived golden (line x colour, one opaque
// sprite pixel per row) generated from video/sprites.vhd under GHDL. See
// _tools/sprite-vhdl-test/gen_sprites.py.
func loadSpriteGolden(t *testing.T) map[[2]int]int {
	t.Helper()
	f, err := os.Open("testdata/sprite_golden.txt")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer func() { _ = f.Close() }()
	g := map[[2]int]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		ln, _ := strconv.Atoi(p[0])
		x, _ := strconv.Atoi(p[1])
		c, _ := strconv.Atoi(p[2])
		g[[2]int{ln, x}] = c
	}
	return g
}

// TestSpriteRenderMatchesFPGAGolden replays a sprite-render scenario whose
// expected pixels were captured from the actual Spectrum Next FPGA sprite
// engine (video/sprites.vhd simulated under GHDL) and asserts ours produces the
// identical rendered raster. This is the sprite analogue of the Z80N CPU golden
// suite: the golden IS the hardware's behaviour, so a green run proves our
// sprite renderer is faithful to the FPGA, not merely plausible. Self-contained
// — no GHDL at run time. Regenerate only when the test scenario changes.
//
// Scenario (8bpp, pattern slot 0 = the identity pattern byte[i]=i, over-border
// on): four 16x16 sprites at Y=48 exercising position, X-mirror, Y-mirror,
// palette offset and transparency. The golden was produced with the FPGA's
// transp_colour set to 0 — matching ours' engine model, where palette index 0
// is transparent (RenderScanline). (The configurable NR$4B transp_colour is a
// known engine-level gap modelled at the compositor, not exercised here.)
func TestSpriteRenderMatchesFPGAGolden(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	e.SetOverBorder(true)

	// pattern slot 0: the identity pattern (byte i = i), 8bpp.
	e.SetPatternAddr(0)
	for i := 0; i < 256; i++ {
		e.WritePatternByte(byte(i))
	}
	e.Set(0, Attr{X: 48, Y: 48, Pattern: 0, Palette: 0, Visible: true})
	e.Set(1, Attr{X: 80, Y: 48, Pattern: 0, Visible: true, XMirror: true})
	e.Set(2, Attr{X: 112, Y: 48, Pattern: 0, Visible: true, YMirror: true})
	e.Set(3, Attr{X: 144, Y: 48, Pattern: 0, Palette: 3, Visible: true})

	golden := loadSpriteGolden(t)
	const width = 320
	dst := make([]byte, width)
	for line := 32; line < 80; line++ {
		for i := range dst {
			dst[i] = 0 // transparent below-sprite content
		}
		e.RenderScanline(line, dst, width)
		for x := 32; x < 176; x++ {
			want, opaque := golden[[2]int{line, x}]
			got := int(dst[x])
			if opaque {
				if got != want {
					t.Errorf("pixel (line=%d,x=%d): got %d, want %d (FPGA golden)", line, x, got, want)
				}
			} else if got != 0 {
				t.Errorf("pixel (line=%d,x=%d): got %d, want transparent (0) — FPGA renders nothing here", line, x, got)
			}
		}
	}
}
