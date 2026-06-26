package layer2

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The Layer 2 golden vectors are captured from the actual Spectrum Next FPGA
// Layer 2 renderer (video/layer2.vhd simulated under GHDL — see
// _tools/layer2-vhdl-test/). Two record kinds:
//
//	ADDR res bank sx sy coord en sram_addr
//	    The effective SRAM byte address (and pixel-enable) the FPGA computes
//	    for raster coordinate coord (= hc*1000 + vc; hc/vc are i_phc/i_pvc for
//	    res 00, i_whc/i_wvc for the wide res 01/1X), with scroll (sx 9-bit,
//	    sy 8-bit), starting bank, all clip windows at reset defaults.
//	PIX  res off sc1 sram_addr pixel
//	    o_layer2_pixel for a fetched byte data = (sram_addr*131+7) mod 256,
//	    with palette offset off; sc1 selects the 4bpp nibble (0 = high,
//	    1 = low) — ignored in 8bpp (res 00/01).
//
// The golden IS the hardware's behaviour, so a green run proves our address
// generation (scroll, the 192-row / 320-column wraps, the bank-effective
// mapping, the A21 ZX-RAM-window guard) and pixel path (palette-offset add,
// 4bpp nibble select) are faithful to the FPGA, not merely plausible.
// Self-contained — no GHDL at run time.

// sramData reconstructs the behavioral framebuffer SRAM the testbench used:
// data(addr) = (addr*131 + 7) mod 256. Keep in sync with tb_layer2.vhd.
func sramData(addr int) byte { return byte((addr*131 + 7) & 0xFF) }

type addrVec struct {
	res, bank, sx, sy, hc, vc int
	en                        bool
	sramAddr                  int
}

type pixVec struct {
	res, off, sc1, sramAddr, pixel int
}

func loadLayer2Golden(t *testing.T) ([]addrVec, []pixVec) {
	t.Helper()
	f, err := os.Open("testdata/layer2_golden.txt")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer func() { _ = f.Close() }()
	var addrs []addrVec
	var pixes []pixVec
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		atoi := func(i int) int { n, _ := strconv.Atoi(p[i]); return n }
		switch p[0] {
		case "ADDR":
			coord := atoi(5)
			addrs = append(addrs, addrVec{
				res: atoi(1), bank: atoi(2), sx: atoi(3), sy: atoi(4),
				hc: coord / 1000, vc: coord % 1000,
				en: atoi(6) == 1, sramAddr: atoi(7),
			})
		case "PIX":
			pixes = append(pixes, pixVec{
				res: atoi(1), off: atoi(2), sc1: atoi(3),
				sramAddr: atoi(4), pixel: atoi(5),
			})
		}
	}
	return addrs, pixes
}

// TestLayer2AddressMatchesFPGAGolden replays each ADDR vector through the
// faithful FPGA address model and asserts the effective SRAM address + enable
// match the FPGA exactly.
func TestLayer2AddressMatchesFPGAGolden(t *testing.T) {
	addrs, _ := loadLayer2Golden(t)
	if len(addrs) == 0 {
		t.Fatal("no ADDR vectors in golden")
	}
	for _, v := range addrs {
		l := New(nil)
		l.SetResolution(byte(v.res))
		l.SetActiveBank(byte(v.bank))
		l.SetScrollX(uint16(v.sx))
		l.SetScrollY(byte(v.sy))
		gotAddr, gotEn := l.fpgaEffectiveAddr(v.hc, v.vc)
		if gotEn != v.en || (v.en && gotAddr != v.sramAddr) {
			t.Errorf("res=%d bank=%d sx=%d sy=%d hc=%d vc=%d: got addr=%d en=%v, want addr=%d en=%v (FPGA golden)",
				v.res, v.bank, v.sx, v.sy, v.hc, v.vc, gotAddr, gotEn, v.sramAddr, v.en)
		}
	}
}

// goldenBanks is a BankReader whose every byte equals the FPGA behavioral SRAM
// value at the corresponding effective address, so RenderScanline (which reads
// via GetPage(bank)[off]) sees exactly what the FPGA fetched. The Layer 2
// effective address maps to our paged model as bank = activeBank + addr16/16K,
// off = addr16 % 16K; for the chosen scenarios we fill banks so that the byte
// at (bank,off) equals sramData(effectiveAddr(bank,off)).
type goldenBanks struct {
	activeBank int
	banks      map[int][]byte
}

func (g *goldenBanks) GetPage(bank int) []byte {
	if g.banks == nil {
		g.banks = map[int][]byte{}
	}
	if g.banks[bank] == nil {
		p := make([]byte, 16384)
		for off := 0; off < 16384; off++ {
			// Reconstruct the FPGA effective address this (bank,off) holds:
			// addr16 = (bank-activeBank)*16K + off; the bankEff offset is
			// applied by fpgaEffectiveAddr, so derive it the same way.
			addr16 := (bank-g.activeBank)*16384 + off
			ab := g.activeBank & 0x7F
			bankEff := (((ab >> 4) + 1) << 4) | (ab & 0x0F)
			addrEff := (((bankEff + ((addr16 >> 14) & 0x7)) & 0xFF) << 14) | (addr16 & 0x3FFF)
			p[off] = sramData(addrEff & 0x1FFFFF)
		}
		g.banks[bank] = p
	}
	return g.banks[bank]
}

// TestLayer2RenderScanlineAppliesFPGAScroll proves RenderScanline applies the
// FPGA scroll + wrap when producing a scanline: each rendered pixel must equal
// the FPGA's o_layer2_pixel for that screen coordinate (derived from the
// golden-verified fpgaEffectiveAddr/fpgaPixel models). This locks in the
// production scroll path (NR$16/$17/$71 -> SetScrollX/Y -> RenderScanline).
func TestLayer2RenderScanlineAppliesFPGAScroll(t *testing.T) {
	type cfg struct {
		res        byte
		bank       int
		sx         uint16
		sy         byte
		off        byte
		rows       []int
		width      int
		halfPixels bool // 640 mode packs 2 px/byte
	}
	cases := []cfg{
		{res: 0, bank: 9, sx: 73, sy: 41, off: 0, rows: []int{0, 1, 63, 95, 191}, width: 256},
		{res: 0, bank: 0, sx: 200, sy: 130, off: 2, rows: []int{0, 64, 130}, width: 256},
		{res: 1, bank: 0, sx: 100, sy: 60, off: 0, rows: []int{0, 33, 200}, width: 320},
		{res: 2, bank: 0, sx: 17, sy: 20, off: 3, rows: []int{0, 20, 99}, width: 640, halfPixels: true},
	}
	for _, c := range cases {
		gb := &goldenBanks{activeBank: c.bank}
		l := New(gb)
		l.SetEnabled(true)
		l.SetResolution(c.res)
		l.SetActiveBank(byte(c.bank))
		l.SetScrollX(c.sx)
		l.SetScrollY(c.sy)
		l.SetPaletteOffset(c.off)
		dst := make([]byte, c.width)
		for _, row := range c.rows {
			for i := range dst {
				dst[i] = 0
			}
			l.RenderScanline(row, dst)
			for x := 0; x < c.width; x++ {
				// Expected: the FPGA address for displayed column hc, row,
				// then the FPGA pixel from that byte.
				var hc, sc1 = x, false
				if c.halfPixels {
					hc = x / 2
					sc1 = x%2 == 1
				}
				// RenderScanline contract: displayed column hc maps to hc_eff=hc.
				addr, en := l.fpgaSramAddr(hc, row)
				var want byte
				if en {
					want = l.fpgaPixel(sramData(addr), sc1)
				}
				if dst[x] != want {
					t.Fatalf("res=%d sx=%d sy=%d off=%d row=%d x=%d: got %d, want %d (FPGA)",
						c.res, c.sx, c.sy, c.off, row, x, dst[x], want)
				}
			}
		}
	}
}

// TestLayer2PixelMatchesFPGAGolden replays each PIX vector through the faithful
// pixel model (palette-offset add + 4bpp nibble select) and asserts equality
// with the FPGA's o_layer2_pixel.
func TestLayer2PixelMatchesFPGAGolden(t *testing.T) {
	_, pixes := loadLayer2Golden(t)
	if len(pixes) == 0 {
		t.Fatal("no PIX vectors in golden")
	}
	for _, v := range pixes {
		l := New(nil)
		l.SetResolution(byte(v.res))
		l.SetPaletteOffset(byte(v.off))
		data := sramData(v.sramAddr)
		got := int(l.fpgaPixel(data, v.sc1 == 1))
		if got != v.pixel {
			t.Errorf("res=%d off=%d sc1=%d data=%#02x: got pixel=%d, want %d (FPGA golden)",
				v.res, v.off, v.sc1, data, got, v.pixel)
		}
	}
}
