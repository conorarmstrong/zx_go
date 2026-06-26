package lores

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goldenVec is one captured FPGA sample: the full lores.vhd input set and the
// three outputs (bank-5 address, palette index, clip enable).
type goldenVec struct {
	cfg         Config
	hc, vc      uint16
	data        uint8
	addr, pixel uint16
	enabled     bool
}

// loadLoResGolden parses the FPGA-derived golden produced from
// video/lores.vhd under GHDL. Column order (see the file header and
// _tools/lores-vhdl-test/tb_lores.vhd):
//
//	mode dfile ulap poff sx sy cx1 cx2 cy1 cy2 hc vc data  addr pixel en
func loadLoResGolden(t *testing.T) []goldenVec {
	t.Helper()
	f, err := os.Open("testdata/lores_golden.txt")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []goldenVec
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		if len(p) != 16 {
			t.Fatalf("golden line has %d fields, want 16: %q", len(p), line)
		}
		n := func(i int) int { v, _ := strconv.Atoi(p[i]); return v }
		v := goldenVec{
			cfg: Config{
				Radastan:      n(0) != 0,
				Dfile:         n(1) != 0,
				ULAPlus:       n(2) != 0,
				PaletteOffset: uint8(n(3)),
				ScrollX:       uint8(n(4)),
				ScrollY:       uint8(n(5)),
				ClipX1:        uint8(n(6)),
				ClipX2:        uint8(n(7)),
				ClipY1:        uint8(n(8)),
				ClipY2:        uint8(n(9)),
			},
			hc:      uint16(n(10)),
			vc:      uint16(n(11)),
			data:    uint8(n(12)),
			addr:    uint16(n(13)),
			pixel:   uint16(n(14)),
			enabled: n(15) != 0,
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan golden: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("golden is empty")
	}
	return out
}

// TestLoResMatchesFPGAGolden replays every captured LoRes/Radastan vector whose
// expected (address, palette index, clip enable) were produced by the actual
// Spectrum Next FPGA LoRes module (video/lores.vhd simulated under GHDL) and
// asserts ours computes the identical outputs. The golden IS the hardware's
// behaviour, so a green run proves our LoRes renderer is faithful to the FPGA,
// not merely plausible. Self-contained — no GHDL at run time.
//
// Coverage (8 scenarios, ~4.3k vectors): plain LoRes; LoRes with X/Y scroll
// (x-wrap + the y>=192 and y>=96 address wraps); LoRes palette-offset hi-nibble
// add with carry; Radastan with dfile/ULA+ palette-offset variants; Radastan
// nibble select (x bit 1) under scroll; and a narrow clip window exercising the
// inclusive clip-enable edges.
func TestLoResMatchesFPGAGolden(t *testing.T) {
	for i, v := range loadLoResGolden(t) {
		addr, pixel, enabled := v.cfg.Pixel(v.hc, v.vc, v.data)
		if uint16(addr) != v.addr {
			t.Errorf("vec %d %+v hc=%d vc=%d data=%d: addr got %d, want %d (FPGA golden)",
				i, v.cfg, v.hc, v.vc, v.data, addr, v.addr)
		}
		if uint16(pixel) != v.pixel {
			t.Errorf("vec %d %+v hc=%d vc=%d data=%d: pixel got %d, want %d (FPGA golden)",
				i, v.cfg, v.hc, v.vc, v.data, pixel, v.pixel)
		}
		if enabled != v.enabled {
			t.Errorf("vec %d %+v hc=%d vc=%d: enabled got %v, want %v (FPGA golden)",
				i, v.cfg, v.hc, v.vc, enabled, v.enabled)
		}
	}
}

// fakeBank5 is a BankReader whose bank 5 is an arbitrary 16 KB pattern; all
// other banks are zero. Used to drive RenderScanline.
type fakeBank5 struct{ b5 []byte }

func (f fakeBank5) GetPage(bank int) []byte {
	if bank == 5 {
		return f.b5
	}
	return make([]byte, 16384)
}

// TestRenderScanlineMatchesPixel proves the scanline renderer composes the
// FPGA address fetch + pixel decode correctly: for each golden vector we plant
// the vector's data byte at the vector's address in bank 5, render that line,
// and assert the emitted pixel matches the golden (for enabled positions) or is
// left untouched (clipped positions). This confirms RenderScanline does the
// bank-5 lookup the FPGA VRAM arbiter performs (zxnext.vhd:4266-4267).
func TestRenderScanlineMatchesPixel(t *testing.T) {
	const sentinel = 0xAA
	for i, v := range loadLoResGolden(t) {
		b5 := make([]byte, 16384)
		b5[v.addr] = v.data
		mem := fakeBank5{b5}
		width := int(v.hc) + 1
		dst := make([]byte, width)
		for j := range dst {
			dst[j] = sentinel
		}
		v.cfg.RenderScanline(mem, int(v.vc), dst, width)
		got := dst[v.hc]
		if v.enabled {
			if uint16(got) != v.pixel {
				t.Errorf("vec %d hc=%d vc=%d: rendered pixel got %d, want %d",
					i, v.hc, v.vc, got, v.pixel)
			}
		} else if got != sentinel {
			t.Errorf("vec %d hc=%d vc=%d: clipped position written (%d), want untouched",
				i, v.hc, v.vc, got)
		}
	}
}
