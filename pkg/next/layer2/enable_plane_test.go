package layer2

import "testing"

// A palette index is a colour, so "no pixel here" cannot be signalled in
// the index plane: index 0 is an ordinary opaque black on a default
// palette. The FPGA carries a separate per-pixel layer2_en beside each
// pixel (video/layer2.vhd:175), and a clipped pixel has it clear — the
// layer contributes nothing there and whatever is beneath shows through.
//
// Returning a bare 0 instead meant the compositor, which decides Layer 2
// transparency by comparing the pixel's COLOUR against NR$14, painted
// opaque black over the lower layers everywhere the clip window
// excluded. Worst case: New() installs the FPGA's $BF reset window, so a
// 320x256 program that never writes NR$18 had rows 192-255 blacked out.
func TestRenderScanlineReportsPerPixelEnable(t *testing.T) {
	l := New(newFakeBanks())
	l.SetEnabled(true)
	l.SetResolution(0)
	l.SetClip(10, 20, 5, 15)

	dst := make([]byte, Width)
	en := make([]byte, Width)
	l.RenderScanlineEnabled(10, dst, en)

	for x, want := range map[int]byte{9: 0, 10: 1, 15: 1, 20: 1, 21: 0} {
		if en[x] != want {
			t.Errorf("row 10, x=%d: enable = %d, want %d", x, en[x], want)
		}
	}

	// A row outside the window is disabled across its whole width.
	for i := range en {
		en[i] = 0xFF
	}
	l.RenderScanlineEnabled(4, dst, en)
	for x := 0; x < Width; x++ {
		if en[x] != 0 {
			t.Fatalf("row 4 is above the clip window but x=%d reports enabled", x)
		}
	}
}

// The default window covers the 256x192 screen, so an unclipped layer
// still reports every pixel enabled.
func TestRenderScanlineEnablesEverythingByDefault(t *testing.T) {
	l := New(newFakeBanks())
	l.SetEnabled(true)
	l.SetResolution(0)

	dst := make([]byte, Width)
	en := make([]byte, Width)
	l.RenderScanlineEnabled(100, dst, en)
	for x := 0; x < Width; x++ {
		if en[x] == 0 {
			t.Fatalf("x=%d disabled with no clip set", x)
		}
	}
}

// A disabled layer produces no pixels at all.
func TestRenderScanlineOnADisabledLayerEnablesNothing(t *testing.T) {
	l := New(newFakeBanks())
	l.SetResolution(0)
	dst := make([]byte, Width)
	en := make([]byte, Width)
	for i := range en {
		en[i] = 0xFF
	}
	l.RenderScanlineEnabled(10, dst, en)
	for x := 0; x < Width; x++ {
		if en[x] != 0 {
			t.Fatalf("x=%d enabled on a disabled layer", x)
		}
	}
}
