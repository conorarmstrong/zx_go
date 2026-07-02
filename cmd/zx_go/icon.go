package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"fyne.io/fyne/v2"
)

// spectrumIcon generates a ZX Spectrum-themed app icon with the classic
// rainbow stripe pattern on a dark background.
func spectrumIcon() fyne.Resource {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Dark background
	bg := color.NRGBA{R: 20, G: 20, B: 30, A: 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}

	// Spectrum rainbow stripes (the classic 8 colours in diagonal bands)
	stripes := []color.NRGBA{
		{R: 0, G: 0, B: 205, A: 255},     // Blue
		{R: 205, G: 0, B: 0, A: 255},     // Red
		{R: 205, G: 0, B: 205, A: 255},   // Magenta
		{R: 0, G: 205, B: 0, A: 255},     // Green
		{R: 0, G: 205, B: 205, A: 255},   // Cyan
		{R: 205, G: 205, B: 0, A: 255},   // Yellow
		{R: 255, G: 255, B: 255, A: 255}, // White
	}

	stripeWidth := 20
	startY := 60
	stripeHeight := 130

	for i, col := range stripes {
		x0 := 30 + i*stripeWidth
		for dy := 0; dy < stripeHeight; dy++ {
			y := startY + dy
			// Diagonal slant
			xOff := dy / 4
			for sx := 0; sx < stripeWidth-2; sx++ {
				px := x0 + sx + xOff
				if px >= 0 && px < size && y >= 0 && y < size {
					img.Set(px, y, col)
				}
			}
		}
	}

	// "ZX" text in white pixels (simple blocky font)
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}

	// Z character (12x14 pixels, at position 85, 205)
	zPattern := []string{
		"############",
		"############",
		"        ## ",
		"       ##  ",
		"      ##   ",
		"     ##    ",
		"    ##     ",
		"   ##      ",
		"  ##       ",
		" ##        ",
		"############",
		"############",
	}
	drawChar(img, zPattern, 75, 210, white)

	// X character
	xPattern := []string{
		"##        ##",
		"###      ###",
		" ###    ### ",
		"  ###  ###  ",
		"   ######   ",
		"    ####    ",
		"   ######   ",
		"  ###  ###  ",
		" ###    ### ",
		"###      ###",
		"##        ##",
		"##        ##",
	}
	drawChar(img, xPattern, 130, 210, white)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return fyne.NewStaticResource("icon.png", buf.Bytes())
}

func drawChar(img *image.RGBA, pattern []string, ox, oy int, col color.Color) {
	scale := 2
	for row, line := range pattern {
		for colIdx, ch := range line {
			if ch == '#' {
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.Set(ox+colIdx*scale+dx, oy+row*scale+dy, col)
					}
				}
			}
		}
	}
}
