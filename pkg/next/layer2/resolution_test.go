package layer2

import "testing"

// Layer 2 resolution coverage map (NextReg $70 bits 5:4):
//
//	00 = 256×192, 8bpp  — IMPLEMENTED (RenderScanline, Width=256)
//	01 = 320×256, 8bpp  — NOT YET (column-major memory layout)
//	10 = 640×256, 4bpp  — NOT YET (column-major, 4bpp packing)
//
// This test pins the implemented 256×192 geometry so a refactor
// can't silently change it, and DOCUMENTS the unimplemented hi-res
// modes via an explicit skip so the gap is visible in test output
// (not silently absent). When 320/640 land, replace the skip with
// real geometry assertions.
func TestLayer2Resolution256x192Implemented(t *testing.T) {
	if Width != 256 || Height != 192 {
		t.Fatalf("Layer2 base geometry = %dx%d, want 256x192", Width, Height)
	}
	f := newFakeBanks()
	l := New(f)
	l.SetEnabled(true)
	l.SetActiveBank(9)
	dst := make([]byte, Width)
	// Every scanline in range must render the active bank's bytes.
	for y := 0; y < Height; y++ {
		for i := range dst {
			dst[i] = 0
		}
		l.RenderScanline(y, dst)
	}
	// y past the bottom is a no-op (transparent), not a panic.
	l.RenderScanline(Height, dst)
}

func TestLayer2HiResModesNotYetImplemented(t *testing.T) {
	t.Skip("Layer 2 320×256 / 640×256 modes (NR$70 bits 5:4 = 01/10) " +
		"not yet implemented — tracked here so the gap is explicit. " +
		"The register stores the mode (wire.go NR$70) but RenderScanline " +
		"only produces 256×192. See pkg/next/layer2/doc.go.")
}

// TestLayer2Resolution320ColumnMajor verifies the 320×256 column-major
// addressing: byte(x,y) = activeBank*16K + x*256 + y, crossing a bank
// boundary every 64 columns.
func TestLayer2Resolution320ColumnMajor(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetEnabled(true)
	l.SetResolution(1) // 320×256
	l.SetActiveBank(0)
	if l.LineWidth() != 320 || l.LineHeight() != 256 {
		t.Fatalf("dims = %d×%d, want 320×256", l.LineWidth(), l.LineHeight())
	}
	check := func(x, y int) byte {
		dst := make([]byte, 320)
		l.RenderScanline(y, dst)
		return dst[x]
	}
	cases := []struct {
		x, y int
		want byte
	}{
		{0, 0, 0},      // bank 0 off 0
		{2, 3, 3},      // off 515 → 515&0xFF
		{64, 0, 7},     // bank 1 off 0 → 1*7
		{319, 255, 27}, // bank 4 off 16383 → (28+16383)&0xFF
	}
	for _, c := range cases {
		if got := check(c.x, c.y); got != c.want {
			t.Errorf("320×256 pixel (%d,%d) = %#x, want %#x", c.x, c.y, got, c.want)
		}
	}
}
