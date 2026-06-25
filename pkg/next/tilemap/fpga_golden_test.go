package tilemap

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goldenPixel is one captured output sample from the FPGA tilemap pipeline:
// the palette index `pixel`, the `en` (pixel-valid) gate, and the `below`
// (cover-ULA) / `text` (textmode) status flags.
type goldenPixel struct {
	en, below, text bool
	pixel           int
}

// loadTilemapGolden reads the FPGA-derived golden (one line per visible
// (scenario,row,col) where the engine emitted en=1 or pixel!=0), generated from
// video/tilemap.vhd under GHDL. See _tools/tilemap-vhdl-test/tb_tilemap.vhd.
// Key: [scenario][row][col].
func loadTilemapGolden(t *testing.T) map[[3]int]goldenPixel {
	t.Helper()
	f, err := os.Open("testdata/tilemap_golden.txt")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer func() { _ = f.Close() }()
	g := map[[3]int]goldenPixel{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		if len(p) != 7 {
			t.Fatalf("bad golden line %q", line)
		}
		scn, _ := strconv.Atoi(p[0])
		row, _ := strconv.Atoi(p[1])
		col, _ := strconv.Atoi(p[2])
		en, _ := strconv.Atoi(p[3])
		bl, _ := strconv.Atoi(p[4])
		tx, _ := strconv.Atoi(p[5])
		px, _ := strconv.Atoi(p[6])
		g[[3]int{scn, row, col}] = goldenPixel{en == 1, bl == 1, tx == 1, px}
	}
	return g
}

// tileDefByte reproduces the tile-pixel formula the testbench writes into the
// tile definitions: pixel(x,y) nibble = 1 + ((x + 4*y) mod 15) (always opaque),
// packed two pixels per byte. Identical to tb_tilemap.vhd's tile-def loop.
func tileDefByte(y, bc int) byte {
	hi := 1 + (((2 * bc) + 4*y) % 15)
	lo := 1 + (((2*bc + 1) + 4*y) % 15)
	return byte(hi<<4 | lo)
}

// expectedDst maps an FPGA golden sample to the palette index ours'
// RenderScanline should write at that display column. en=0 (clipped /
// transparent) -> 0; otherwise the captured pixel value. The below/text status
// bits are compositor-level concerns outside RenderScanline's palette-index
// output, so they are validated structurally (see TestTilemapBelowTextStatus)
// rather than per-pixel here.
func expectedDst(gp goldenPixel) byte {
	if !gp.en {
		return 0
	}
	return byte(gp.pixel)
}

// TestTilemapRenderMatchesFPGAGolden replays nine tilemap-render scenarios whose
// expected rasters were captured from the actual Spectrum Next FPGA tilemap
// engine (video/tilemap.vhd simulated under GHDL) and asserts ours produces the
// identical palette-indexed scanline. The golden IS the hardware's behaviour, so
// a green run proves our tilemap renderer is faithful to the FPGA, not merely
// plausible. Self-contained — no GHDL at run time. Regenerate only when the test
// scenarios change.
//
// Scenarios (see testdata header): 40x32 strip_flags, per-tile attribute
// transforms (palette offset / X-mirror / Y-mirror / rotate), 80x32, pixel
// scroll, clip window, 1bpp textmode, 512-tile mode, and bank-7 selection.
func TestTilemapRenderMatchesFPGAGolden(t *testing.T) {
	golden := loadTilemapGolden(t)

	// Shared tile definitions at bank-5 offset $0500 (4bpp, 32 B/tile),
	// tiles 1..4 identical to the testbench formula.
	buildTileDefs := func(buf []byte, base int) {
		for tile := 1; tile <= 4; tile++ {
			for y := 0; y < 8; y++ {
				for bc := 0; bc < 4; bc++ {
					buf[base+tile*32+y*4+bc] = tileDefByte(y, bc)
				}
			}
		}
	}

	// assertScenario renders `rows` through ours' API and compares every
	// visible column [0,maxcol] against the golden.
	assertScenario := func(t *testing.T, scn int, tm *Tilemap, width int, maxcol int, rows []int) {
		t.Helper()
		for _, row := range rows {
			dst := make([]byte, width)
			tm.RenderScanline(row, dst)
			for col := 0; col <= maxcol && col < width; col++ {
				want := byte(0)
				if gp, ok := golden[[3]int{scn, row, col}]; ok {
					want = expectedDst(gp)
				}
				if dst[col] != want {
					t.Errorf("scenario %d row %d col %d: got %d, want %d (FPGA golden)",
						scn, row, col, dst[col], want)
				}
			}
		}
	}

	// --- Scenario 1: 40x32, strip_flags, default_attr $00 ---
	t.Run("1_40x32_strip_flags", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		for r := 0; r < 32; r++ {
			for c := 0; c < 40; c++ {
				buf[r*40+c] = byte(((r*40 + c) % 4) + 1)
			}
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x00)
		tm.SetTilesBase(0x05)
		tm.SetControl(0x20) // strip_flags, 40col
		tm.SetDefaultAttr(0x00)
		tm.SetEnabled(true)
		assertScenario(t, 1, tm, 320, 319, []int{0, 1, 7, 9})
	})

	// --- Scenario 2: 40x32, per-tile attrs (palette/mirror/rotate) ---
	t.Run("2_per_tile_attrs", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		const mb = 0x1000
		for c := 0; c < 40; c++ {
			buf[mb+c*2] = 0x01 // tile id 1
		}
		attrs := []byte{0x00, 0x10, 0x20, 0x30, 0x08, 0x04, 0x02, 0x0C, 0x22, 0x0E}
		for c, a := range attrs {
			buf[mb+c*2+1] = a
		}
		for c := 10; c < 40; c++ {
			buf[mb+c*2+1] = byte((c % 4) * 16)
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x10) // $1000
		tm.SetTilesBase(0x05)
		tm.SetControl(0x00) // 40col, attrs present, 4bpp
		tm.SetEnabled(true)
		assertScenario(t, 2, tm, 320, 319, []int{0, 1, 2, 7})
	})

	// --- Scenario 3: 80x32, strip_flags ---
	t.Run("3_80x32", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		const mb = 0x2000
		for c := 0; c < 80; c++ {
			buf[mb+c] = byte((c % 4) + 1)
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x20) // $2000
		tm.SetTilesBase(0x05)
		tm.SetControl(0x60) // 80col(bit6)+strip_flags(bit5)
		tm.SetEnabled(true)
		// 80col is a 640px-wide surface; the engine emits both 14MHz
		// sub-pixels per 7MHz hcounter, so render through a 640px buffer with
		// display column = source pixel.
		assertScenario(t, 3, tm, 640, 639, []int{0, 5})
	})

	// --- Scenario 4: scroll ---
	t.Run("4_scroll", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		for r := 0; r < 32; r++ {
			for c := 0; c < 40; c++ {
				buf[r*40+c] = byte(((r*40 + c) % 4) + 1)
			}
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x00)
		tm.SetTilesBase(0x05)
		tm.SetControl(0x20) // strip_flags, 40col
		tm.SetEnabled(true)
		tm.SetScrollX(8)
		tm.SetScrollY(8)
		assertScenario(t, 4, tm, 320, 319, []int{0})
		tm.SetScrollX(3)
		tm.SetScrollY(5)
		assertScenario(t, 4, tm, 320, 319, []int{1})
	})

	// --- Scenario 5: clip window x1=8 x2=20 y1=4 y2=12 ---
	t.Run("5_clip", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		for r := 0; r < 32; r++ {
			for c := 0; c < 40; c++ {
				buf[r*40+c] = byte(((r*40 + c) % 4) + 1)
			}
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x00)
		tm.SetTilesBase(0x05)
		tm.SetControl(0x20)
		tm.SetEnabled(true)
		tm.SetClip(8, 20, 4, 12)
		assertScenario(t, 5, tm, 320, 319, []int{3, 4, 12, 13})
	})

	// --- Scenario 6: textmode (1bpp) ---
	t.Run("6_textmode", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		// 1bpp tile 1 at tilesBase $0700: each row = $CA.
		for y := 0; y < 8; y++ {
			buf[0x700+1*8+y] = 0xCA
		}
		const mb = 0x3000
		for c := 0; c < 40; c++ {
			buf[mb+c*2] = 0x01
			buf[mb+c*2+1] = 0x34
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x30) // $3000
		tm.SetTilesBase(0x07)   // $0700
		tm.SetControl(0x08)     // textmode(bit3), 40col, attrs present
		tm.SetEnabled(true)
		assertScenario(t, 6, tm, 320, 319, []int{0, 1})
	})

	// --- Scenario 7: 512-tile mode ---
	t.Run("7_512_tile", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		// tile 256 def at $0500 + 256*32 = $2500.
		for y := 0; y < 8; y++ {
			for bc := 0; bc < 4; bc++ {
				buf[0x2500+y*4+bc] = tileDefByte(y, bc)
			}
		}
		const mb = 0x3800
		for c := 0; c < 40; c++ {
			buf[mb+c*2] = 0x00   // tile low
			buf[mb+c*2+1] = 0x01 // attr bit0 = MSB
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x38) // $3800
		tm.SetTilesBase(0x05)
		tm.SetControl(0x02) // mode_512(bit1), 40col, attrs present
		tm.SetEnabled(true)
		assertScenario(t, 7, tm, 320, 319, []int{0})
	})

	// --- Scenario 8: bank 7 selection ---
	t.Run("8_bank7", func(t *testing.T) {
		bank7 := make([]byte, 0x4000)
		for c := 0; c < 40; c++ {
			bank7[c] = byte((c % 4) + 1)
		}
		buildTileDefs(bank7, 0x500)
		// bank 5 holds DIFFERENT content so a wrong-bank read fails loudly.
		bank5 := make([]byte, 0x4000)
		for i := range bank5 {
			bank5[i] = 0xEE
		}
		tm := New(&twoBank{bank5: bank5, bank7: bank7})
		tm.SetTileMapBase(0xC0) // bit7 -> bank 7, offset 0
		tm.SetTilesBase(0xC5)   // bit7 -> bank 7, offset $0500
		tm.SetControl(0x20)     // strip_flags, 40col
		tm.SetEnabled(true)
		assertScenario(t, 8, tm, 320, 319, []int{0})
	})

	// --- Scenarios 9 & 10: on_top / below — pixel raster is identical
	// regardless of on_top (on_top only changes the below status bit, which
	// RenderScanline does not emit). Assert the palette raster matches both. ---
	t.Run("9_10_ontop_below_raster", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		for r := 0; r < 32; r++ {
			for c := 0; c < 40; c++ {
				buf[r*40+c] = byte(((r*40 + c) % 4) + 1)
			}
		}
		// scenario 9: on_top set, default_attr bit0=1
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x00)
		tm.SetTilesBase(0x05)
		tm.SetControl(0x21) // strip_flags + on_top
		tm.SetDefaultAttr(0x01)
		tm.SetEnabled(true)
		assertScenario(t, 9, tm, 320, 319, []int{0})
		// scenario 10: same, on_top clear
		tm.SetControl(0x20)
		assertScenario(t, 10, tm, 320, 319, []int{0})
	})

	// --- Scenarios 11 & 12: transparency is independent of on_top. A tile with
	// a nibble-0 (transparent) first pixel must render transparent (dst=0) at
	// that column under BOTH on_top set and clear — on_top only clears the
	// below stencil, it does NOT make index 0 opaque at the engine level. ---
	t.Run("11_12_transparency_vs_ontop", func(t *testing.T) {
		buf := make([]byte, 0x4000)
		buildTileDefs(buf, 0x500)
		// tile 5: same formula, then overwrite row0 byte0 -> pix0 nibble 0.
		for y := 0; y < 8; y++ {
			for bc := 0; bc < 4; bc++ {
				buf[0x500+5*32+y*4+bc] = tileDefByte(y, bc)
			}
		}
		buf[0x500+5*32+0] = 0x02 // pix0 transparent, pix1 nibble 2
		const mb = 0x3C00
		for c := 0; c < 40; c++ {
			buf[mb+c] = 0x05
		}
		tm := New(&fakeBank{data: buf})
		tm.SetTileMapBase(0x3C) // $3C00
		tm.SetTilesBase(0x05)
		tm.SetDefaultAttr(0x00)
		tm.SetEnabled(true)
		// scenario 11: on_top ON
		tm.SetControl(0x21)
		assertScenario(t, 11, tm, 320, 319, []int{0})
		// scenario 12: on_top OFF
		tm.SetControl(0x20)
		assertScenario(t, 12, tm, 320, 319, []int{0})
		// belt-and-braces: the transparent first pixel of every tile is 0 in
		// both modes (cols 0, 8, 16, ...).
		dst := make([]byte, 320)
		tm.SetControl(0x21)
		tm.RenderScanline(0, dst)
		for x := 0; x < 320; x += 8 {
			if dst[x] != 0 {
				t.Errorf("on_top: col %d = %d, want 0 (index-0 stays transparent)", x, dst[x])
			}
		}
	})
}

// TestTilemapBelowTextStatusFPGAGolden validates the per-pixel status bits the
// FPGA emits alongside the palette index — pixel_below_o (cover-ULA) and
// pixel_textmode_o — against the same golden. These bits are compositor-level
// concerns (RenderScanline emits only the palette index), so rather than replay
// them through the render path we assert the captured bits equal the analytical
// hardware value for each scenario's configuration. This makes the golden's
// below/text columns load-bearing and pins the FPGA semantics:
//
//	below = (tile_attr(0) OR mode_512) AND NOT on_top   (tilemap.vhd:388)
//	text  = the textmode control bit for that tile      (tilemap.vhd:426)
//
// (Documented coverage gap: ours' Tilemap exposes on_top via OnTop() and the
// textmode/512 bits via SetControl; the per-pixel below stencil is applied by
// the compositor, not this layer, so it has no RenderScanline accessor to drive
// here.)
func TestTilemapBelowTextStatusFPGAGolden(t *testing.T) {
	golden := loadTilemapGolden(t)

	// expected (below, text) per scenario, for any opaque (en=1) pixel.
	type want struct{ below, text bool }
	cases := map[int]want{
		1:  {false, false}, // strip_flags, default_attr $00 -> attr(0)=0, no on_top
		2:  {false, false}, // per-tile attrs, all attr(0)=0 in this row set
		3:  {false, false}, // 80x32 strip_flags, default_attr $00
		4:  {false, false}, // scroll, default_attr $00
		5:  {false, false}, // clip, default_attr $00
		6:  {false, true},  // textmode -> text=1; attr $34 bit0=0 -> below=0
		7:  {true, false},  // 512-tile -> mode_512=1 -> below=1, no on_top
		8:  {false, false}, // bank7 strip_flags, default_attr $00
		9:  {false, false}, // on_top + attr(0)=1 -> (1 or 0) and not 1 = 0
		10: {true, false},  // attr(0)=1, no on_top -> below=1
		11: {false, false}, // transparency test, default_attr $00, on_top
		12: {false, false}, // transparency test, default_attr $00, no on_top
	}
	for scn, w := range cases {
		found := false
		for key, gp := range golden {
			if key[0] != scn || !gp.en {
				continue
			}
			found = true
			if gp.below != w.below || gp.text != w.text {
				t.Errorf("scenario %d (row %d col %d): below=%v text=%v, want below=%v text=%v (FPGA golden)",
					scn, key[1], key[2], gp.below, gp.text, w.below, w.text)
			}
		}
		if !found {
			t.Errorf("scenario %d: no en=1 sample in golden", scn)
		}
	}
}
