package debugger

import (
	"image"
	"image/color"
)

// RenderIndexed turns a palette-indexed framebuffer (rows of byte
// indices, row-major) into an *image.RGBA via colorOf. Pure function —
// the shared pixel engine behind the Layer-2 framebuffer and tilemap
// viewers. Empty input yields a 1×1 image so callers needn't special-
// case it.
func RenderIndexed(rows [][]byte, colorOf func(byte) color.RGBA) *image.RGBA {
	h := len(rows)
	w := 0
	if h > 0 {
		w = len(rows[0])
	}
	if w == 0 || h == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := rows[y]
		for x := 0; x < w && x < len(row); x++ {
			img.SetRGBA(x, y, colorOf(row[x]))
		}
	}
	return img
}
