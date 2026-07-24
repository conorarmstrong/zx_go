package main

import (
	"image"
	"image/color"
	"testing"
)

// The View presets exist to give an exact whole-number pixel multiple: "300%"
// must mean each ZX pixel is a crisp 3x3 block. But the window is not the
// content area — fyne puts the main menu above it and pads around it — so
// sizing the WINDOW to 960x720 leaves a 952x742 content box, and 952/320 =
// 2.975 floors to 2. The user asks for 300% and silently gets 200%, with the
// lost third of the window showing as a thick surround.
//
// Measured on a real run before this fix: container 952x742, factor 2, image
// 640x480 inside a 1904x1556 (retina) window.
func TestWindowSizeForFrameLeavesAnExactContentBox(t *testing.T) {
	const menuH, pad = 30, 4
	for _, mult := range []int{1, 2, 3, 4} {
		wantW, wantH := float32(320*mult), float32(240*mult)
		winW, winH := windowSizeForFrame(wantW, wantH, menuH, pad)

		// Strip the chrome back off: what the layout will actually receive.
		gotW := winW - 2*pad
		gotH := winH - menuH - 2*pad
		if gotW != wantW || gotH != wantH {
			t.Errorf("%d00%%: content box = %gx%g, want %gx%g", mult, gotW, gotH, wantW, wantH)
		}
		if f := integerScaleFactor(gotW, gotH, 320, 240, 1); f != mult {
			t.Errorf("%d00%%: factor = %d, want %d", mult, f, mult)
		}
	}
}

// An exact multiple must not be lost to float32 rounding: 960/320 is 3.0 in
// real arithmetic, but a float32 division landing on 2.9999997 would floor to
// 2 and halve the image. Guard the exact boundaries the presets land on.
func TestIntegerScaleFactorExactBoundaries(t *testing.T) {
	for _, mult := range []int{1, 2, 3, 4, 5, 6, 8} {
		w, h := float32(320*mult), float32(240*mult)
		if got := integerScaleFactor(w, h, 320, 240, 1); got != mult {
			t.Errorf("container %gx%g: factor = %d, want %d", w, h, got, mult)
		}
		// Same, reached through a HiDPI density rather than a bigger container.
		if got := integerScaleFactor(320, 240, 320, 240, float32(mult)); got != mult {
			t.Errorf("density %d: factor = %d, want %d", mult, got, mult)
		}
	}
}

// The gap between the emulated frame and the window edge should be the border
// colour, not black. Our 320x240 frame is itself a crop of the full ULA frame,
// the bulk of which is border, so continuing the border out to the window edge
// is what a real Spectrum on a TV shows — and it makes any residual gap
// invisible instead of framing the picture in black.
func TestFrameBorderColorSamplesTheOutermostPixel(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	border := color.RGBA{0xCD, 0xCD, 0xCD, 0xFF} // 48K boot: white border
	paper := color.RGBA{0x00, 0x00, 0xCD, 0xFF}
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, border)
		}
	}
	// Paper block inset from the border, as on a real frame.
	for y := 24; y < 216; y++ {
		for x := 32; x < 288; x++ {
			img.Set(x, y, paper)
		}
	}

	got := frameBorderColor(img)
	if got != color.Color(border) {
		t.Errorf("frameBorderColor = %v, want the border %v", got, border)
	}

	// A nil image must not panic; black is the safe fallback.
	if got := frameBorderColor(nil); got != color.Color(color.Black) {
		t.Errorf("frameBorderColor(nil) = %v, want black", got)
	}
}
