package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

type clipFakeBank struct{ data []byte }

func (f *clipFakeBank) GetPage(int) []byte { return f.data }

// The NR$18/$19/$1B write handlers push the window down into the layer
// that renders with it. LoadState restored only the register copies, so
// a rewind across a clip-window change put the registers back and left
// every layer clipping to the window the running program had set.
func TestLoadStateRestoresTheWindowsIntoTheLayers(t *testing.T) {
	newRig := func() (*nextregs.Dispatcher, *ClipWindows, *layer2.Layer2, *sprite.Engine, *tilemap.Tilemap) {
		d := nextregs.New()
		l2 := layer2.New(&clipFakeBank{data: make([]byte, 0x4000)})
		spr := sprite.New()
		tm := tilemap.New(&clipFakeBank{data: make([]byte, 0x4000)})
		cw := WireClipWindows(d, tm, spr, l2)
		return d, cw, l2, spr, tm
	}

	// Capture a machine whose windows are all at their reset values.
	_, cwA, _, _, _ := newRig()
	saved := cwA.SaveState()

	// A second machine where the guest has narrowed every window.
	dB, cwB, l2B, sprB, tmB := newRig()
	for _, reg := range []byte{0x18, 0x19, 0x1B} {
		for _, v := range []byte{20, 40, 30, 60} {
			dB.WriteReg(reg, v)
		}
	}
	if x1, _, _, _ := l2B.Clip(); x1 != 20 {
		t.Fatalf("setup: layer 2 clip x1 = %d, want 20", x1)
	}

	if err := cwB.LoadState(saved); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if x1, x2, y1, y2 := l2B.Clip(); x1 != 0x00 || x2 != 0xFF || y1 != 0x00 || y2 != 0xBF {
		t.Errorf("layer 2 clip after restore = %d,%d,%d,%d, want 0,255,0,191", x1, x2, y1, y2)
	}
	if x1, x2, y1, y2, _ := sprB.Clip(); x1 != 0x00 || x2 != 0xFF || y1 != 0x00 || y2 != 0xBF {
		t.Errorf("sprite clip after restore = %d,%d,%d,%d, want 0,255,0,191", x1, x2, y1, y2)
	}
	if x1, x2, y1, y2 := tmB.Clip(); x1 != 0x00 || x2 != 0x9F || y1 != 0x00 || y2 != 0xFF {
		t.Errorf("tilemap clip after restore = %d,%d,%d,%d, want 0,159,0,255", x1, x2, y1, y2)
	}
}
