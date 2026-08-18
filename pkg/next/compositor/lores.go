package compositor

import (
	"image/color"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// The LoRes / Radastan layer, as the video pipeline sees it.
//
// LoRes is not a layer that composites OVER the ULA. It REPLACES the ULA
// pixel, per pixel, before anything else in the pipeline runs:
//
//	ulalores_pixel_1 <= lores_pixel_1 when lores_pixel_en_1 = '1'
//	                    else ula_pixel_1;              -- zxnext.vhd:6980
//
// and the result then goes through the ULA/tilemap palette
// (`ulatm_pixel_1 <= '0' & ula_palette_select_1 & ulalores_pixel_1`, :6981),
// which is why this is a ULA-row substitution rather than another entry in the
// mixer. Everything downstream — Layer 2, tilemap, sprites, the transparency
// and priority rules — sees whatever this leaves behind and needs no changes.
//
// The gate is the layer's own clip AND the NR$15 master enable:
//
//	lores_pixel_en_1 <= lores_pixel_en_1a and lores_en_1;   -- zxnext.vhd:6933
//
// so where the clip rejects a pixel, or the layer is off, the ULA's own pixel
// survives untouched. That is exactly the contract lores.RenderScanline
// documents for the positions it skips, and it is why this whole path is inert
// while a guest leaves NR$15 bit 7 clear.

// LoResEnable reports the layer's master enable. pkg/next.LoResState satisfies
// it; the interface lives here so this package does not import pkg/next.
type LoResEnable interface{ Enabled() bool }

// LoRes renders the layer over a ULA row.
type LoRes struct {
	cfg *lores.Config
	en  LoResEnable
	mem lores.BankReader
	pal *palette.Bank
}

// NewLoRes wires the layer to its config, its master enable, the RAM it reads
// its display from, and the palette its indices resolve through.
func NewLoRes(cfg *lores.Config, en LoResEnable, mem lores.BankReader, pal *palette.Bank) *LoRes {
	return &LoRes{cfg: cfg, en: en, mem: mem, pal: pal}
}

// Active reports whether the layer would replace any ULA pixel at all. The ULA
// skips the substitution pass entirely when this is false, so a machine with
// LoRes off renders exactly as it did before the layer existed.
func (l *LoRes) Active() bool {
	return l != nil && l.cfg != nil && l.mem != nil && l.pal != nil &&
		l.en != nil && l.en.Enabled()
}

// ComposeULARow replaces row's pixels with the layer's, for paper row y, at
// every position the clip window admits. row is the 256 pixels of the classic
// screen area and arrives holding the ULA's own colours; positions the layer
// does not claim are left exactly as they came in.
func (l *LoRes) ComposeULARow(y int, row []color.RGBA) {
	if !l.Active() {
		return
	}
	bank5 := l.mem.GetPage(5)
	ulaPal := l.pal.PaletteForLayer(palette.LayerULA)
	vc := uint16(y)

	for x := 0; x < len(row); x++ {
		hc := uint16(x)
		// address first, then the byte, then the pixel: addr depends only on
		// the raster position and the config, never on the data, which is why
		// lores.Config exposes the two halves separately.
		addr := l.cfg.Address(hc, vc)
		var data uint8
		if int(addr) < len(bank5) {
			data = bank5[addr]
		}
		_, idx, enabled := l.cfg.Pixel(hc, vc, data)
		if !enabled {
			continue
		}
		r, g, b := ulaPal.RGB(idx)
		row[x] = color.RGBA{R: r, G: g, B: b, A: 255}
	}
}
