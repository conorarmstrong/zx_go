package debugger

import (
	"image"
	"image/color"
)

// SpriteDim is the edge length of a Next sprite in pixels.
const SpriteDim = 16

// SpritePixels is the pixel count of one 16×16 sprite (row-major).
const SpritePixels = SpriteDim * SpriteDim

// RenderSpriteSheet tiles pre-resolved sprites into a sheet: each
// sprite is SpritePixels RGBA values (row-major 16×16), drawn at the
// given integer `scale`, `cols` per row. Pure function — the provider
// resolves 4bpp + palette to RGBA; this just lays them out. Foundation
// for the visual sprite viewer (jnext's marquee inspector).
func RenderSpriteSheet(sprites [][SpritePixels]color.RGBA, scale, cols int) *image.RGBA {
	if scale < 1 {
		scale = 1
	}
	if cols < 1 {
		cols = 1
	}
	cell := SpriteDim * scale
	rows := (len(sprites) + cols - 1) / cols
	if rows < 1 {
		rows = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, cols*cell, rows*cell))
	for k, sp := range sprites {
		ox := (k % cols) * cell
		oy := (k / cols) * cell
		for py := 0; py < SpriteDim; py++ {
			for px := 0; px < SpriteDim; px++ {
				c := sp[py*SpriteDim+px]
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						img.SetRGBA(ox+px*scale+sx, oy+py*scale+sy, c)
					}
				}
			}
		}
	}
	return img
}
