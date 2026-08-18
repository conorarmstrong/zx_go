package compositor

import (
	"image/color"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

type fakeEnable struct{ on bool }

func (f *fakeEnable) Enabled() bool { return f.on }

// fakeBank5 serves one 16 KB bank of a known pattern as bank 5.
type fakeBank5 struct{ data []byte }

func (f *fakeBank5) GetPage(bank int) []byte {
	if bank != 5 {
		return nil
	}
	return f.data
}

func newLoResUnderTest(t *testing.T) (*LoRes, *lores.Config, *fakeEnable, *palette.Bank) {
	t.Helper()
	cfg := &lores.Config{ClipX2: 0xFF, ClipY2: 0xBF} // the FPGA reset window
	en := &fakeEnable{}
	mem := &fakeBank5{data: make([]byte, 16384)}
	for i := range mem.data {
		mem.data[i] = byte(i)
	}
	pal := palette.NewBank()
	// A palette where entry n is a distinct colour, so a pixel resolving
	// through the wrong index is visible rather than plausible.
	for i := 0; i < 256; i++ {
		pal.PaletteForLayer(palette.LayerULA).Set(byte(i), uint16(i)&0x1FF)
	}
	return NewLoRes(cfg, en, mem, pal), cfg, en, pal
}

func ulaRow() []color.RGBA {
	row := make([]color.RGBA, 256)
	for i := range row {
		row[i] = color.RGBA{R: 1, G: 2, B: 3, A: 255} // a colour the layer never produces
	}
	return row
}

// With the master enable clear, nothing is touched. This is the property that
// makes the whole path safe to add: a machine that never enables LoRes renders
// exactly as it did before the layer existed.
func TestTheLayerIsInertWhileTheMasterEnableIsClear(t *testing.T) {
	l, _, en, _ := newLoResUnderTest(t)
	en.on = false

	row := ulaRow()
	l.ComposeULARow(0, row)

	for x, px := range row {
		if px != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
			t.Fatalf("pixel %d was replaced with %v while the layer was disabled", x, px)
		}
	}
	if l.Active() {
		t.Error("Active reports true with the master enable clear")
	}
}

// Enabled, the layer replaces every pixel the clip admits.
func TestTheLayerReplacesTheULAPixelsItClaims(t *testing.T) {
	l, cfg, en, pal := newLoResUnderTest(t)
	en.on = true

	row := ulaRow()
	l.ComposeULARow(3, row)

	// Spot-check against the layer's own derivation rather than a transcribed
	// constant, so this tests the wiring and the palette lookup, and the FPGA
	// goldens in pkg/next/lores keep testing the derivation itself.
	mem := &fakeBank5{data: make([]byte, 16384)}
	for i := range mem.data {
		mem.data[i] = byte(i)
	}
	ulaPal := pal.PaletteForLayer(palette.LayerULA)
	for _, x := range []int{0, 1, 17, 128, 255} {
		addr := cfg.Address(uint16(x), 3)
		_, idx, enabled := cfg.Pixel(uint16(x), 3, mem.data[addr])
		if !enabled {
			t.Fatalf("x=%d is clipped under the reset window, so this check is wrong", x)
		}
		r, g, b := ulaPal.RGB(idx)
		if got := row[x]; got != (color.RGBA{R: r, G: g, B: b, A: 255}) {
			t.Errorf("x=%d: pixel = %v, want %v (palette index %d)", x, got,
				color.RGBA{R: r, G: g, B: b, A: 255}, idx)
		}
	}
}

// Where the clip window rejects a pixel the ULA's own survives, which is the
// pre-fill contract lores.RenderScanline documents and the FPGA's
// `lores_pixel_en` gate (zxnext.vhd:6933).
func TestClippedPixelsKeepTheULAColourUnderneath(t *testing.T) {
	l, cfg, en, _ := newLoResUnderTest(t)
	en.on = true
	cfg.ClipX1, cfg.ClipX2 = 10, 20

	row := ulaRow()
	l.ComposeULARow(0, row)

	untouched := color.RGBA{R: 1, G: 2, B: 3, A: 255}
	for x := 0; x < 10; x++ {
		if row[x] != untouched {
			t.Fatalf("pixel %d left of the clip window was replaced", x)
		}
	}
	for x := 10; x <= 20; x++ {
		if row[x] == untouched {
			t.Fatalf("pixel %d inside the clip window was not replaced", x)
		}
	}
	for x := 21; x < 256; x++ {
		if row[x] != untouched {
			t.Fatalf("pixel %d right of the clip window was replaced", x)
		}
	}
}

// Radastan is a different mode, not a different palette offset: it packs two
// pixels per byte, so the same row must come out differently from LoRes.
func TestRadastanDiffersFromLoRes(t *testing.T) {
	l, cfg, en, _ := newLoResUnderTest(t)
	en.on = true

	plain := ulaRow()
	l.ComposeULARow(5, plain)

	cfg.Radastan = true
	radastan := ulaRow()
	l.ComposeULARow(5, radastan)

	same := true
	for x := range plain {
		if plain[x] != radastan[x] {
			same = false
			break
		}
	}
	if same {
		t.Error("Radastan produced the same row as LoRes: the mode bit is not reaching " +
			"the pixel derivation")
	}
}

// The scroll offsets move the picture. Without this the registers could be
// wired and stored and still never reach the address derivation.
func TestTheScrollOffsetsMoveThePicture(t *testing.T) {
	l, cfg, en, _ := newLoResUnderTest(t)
	en.on = true

	before := ulaRow()
	l.ComposeULARow(7, before)

	cfg.ScrollX = 9
	after := ulaRow()
	l.ComposeULARow(7, after)

	same := true
	for x := range before {
		if before[x] != after[x] {
			same = false
			break
		}
	}
	if same {
		t.Error("the horizontal scroll offset did not move the picture")
	}
}
