package debugger

import (
	"image"
	"image/color"
)

// RenderSwatch draws `count` colours into a grid of `cols` columns,
// each cell `cell`×`cell` pixels, filling every cell with the colour
// returned by colorAt(i). Entries flow left-to-right, top-to-bottom.
// Pure function — the pixel engine behind the visual palette / sprite
// inspectors. Returns an *image.RGBA the GUI wraps in a canvas.Image
// (or a CLI writes out as PNG).
func RenderSwatch(colorAt func(i int) color.RGBA, count, cell, cols int) *image.RGBA {
	if cols < 1 {
		cols = 1
	}
	if cell < 1 {
		cell = 1
	}
	rows := (count + cols - 1) / cols
	if rows < 1 {
		rows = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, cols*cell, rows*cell))
	for i := 0; i < count; i++ {
		cx := (i % cols) * cell
		cy := (i / cols) * cell
		c := colorAt(i)
		for y := cy; y < cy+cell; y++ {
			for x := cx; x < cx+cell; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	return img
}
