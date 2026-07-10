package main

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
)

// TestIntegerScaleFactor pins the crisp-pixel scaling maths: given a container
// and the native 320x240 frame, pick the largest whole-number pixel multiple
// that fits, measured in PHYSICAL pixels (logical size x canvas density). This
// is what keeps ZX pixels square on HiDPI / fractionally-scaled displays.
func TestIntegerScaleFactor(t *testing.T) {
	const srcW, srcH = 320, 240
	cases := []struct {
		name                   string
		containerW, containerH float32
		density                float32
		want                   int
	}{
		{"native 1:1, no hidpi", 320, 240, 1, 1},
		{"native size, retina 2x density", 320, 240, 2, 2},
		{"200% window, no hidpi", 640, 480, 1, 2},
		{"300% window, no hidpi", 960, 720, 1, 3},
		{"400% window, no hidpi", 1280, 960, 1, 4},
		// 125% window is a fractional multiple of 320x240 — floors to 1x
		// (crisp, centred with bars) instead of a soft 1.25x stretch.
		{"125% window floors to 1x", 400, 300, 1, 1},
		// A 125% OS display-scale at a native-logical window still yields a
		// crisp 1x: 320 logical x 1.25 = 400 physical, one whole 320-block.
		{"125% os scale at native window", 320, 240, 1.25, 1},
		{"150% os scale at 200% window", 640, 480, 1.5, 3},
		// Never vanish: a container smaller than one native frame clamps to 1x.
		{"below native clamps to 1x", 100, 100, 1, 1},
		// Defensive: non-positive density is treated as 1.
		{"zero density defaults to 1", 640, 480, 0, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := integerScaleFactor(c.containerW, c.containerH, srcW, srcH, c.density)
			if got != c.want {
				t.Errorf("integerScaleFactor(%g,%g,%d,%d,%g) = %d, want %d",
					c.containerW, c.containerH, srcW, srcH, c.density, got, c.want)
			}
		})
	}
}

// TestIntegerScaleLayoutCentres checks the layout sizes the image to the
// integer-scaled frame and centres it, leaving symmetric black bars.
func TestIntegerScaleLayoutCentres(t *testing.T) {
	l := &integerScaleLayout{srcW: 320, srcH: 240} // nil canvas => density 1
	obj := canvas.NewRectangle(color.Black)

	// 700x480 container: factor 2 (limited by height), image 640x480, so
	// 30px pillarbox bars left/right and a snug fit top/bottom.
	l.Layout([]fyne.CanvasObject{obj}, fyne.NewSize(700, 480))
	if got := obj.Size(); got.Width != 640 || got.Height != 480 {
		t.Errorf("image size = %v, want 640x480", got)
	}
	if got := obj.Position(); got.X != 30 || got.Y != 0 {
		t.Errorf("image pos = %v, want (30,0)", got)
	}

	// Empty object list must not panic.
	l.Layout(nil, fyne.NewSize(640, 480))
}

func TestScreenLayoutForSelectsLayout(t *testing.T) {
	if _, ok := screenLayoutFor(true, nil).(*integerScaleLayout); !ok {
		t.Error("integer=true should give *integerScaleLayout")
	}
	if _, ok := screenLayoutFor(false, nil).(*aspectRatioLayout); !ok {
		t.Error("integer=false should give *aspectRatioLayout")
	}
}
