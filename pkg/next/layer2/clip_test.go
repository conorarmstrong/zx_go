package layer2

import "testing"

// The FPGA clips Layer 2 per pixel (video/layer2.vhd:167):
//
//	layer2_clip_en <= '1' when (hc_eff >= clip_x1_q) and (hc_eff <= clip_x2_q)
//	                       and (vc_eff >= clip_y1_q) and (vc_eff <= clip_y2_q) ...
//	layer2_en <= '1' when layer2_en_q = '1' and layer2_clip_en = '1' and ...
//
// The coordinates arrive from NextReg $18. Sprites ($19), ULA ($1A) and
// the tilemap ($1B) each had their window pushed into the layer;
// Layer 2's was stored in the wire layer and never reached the layer at
// all, so Layer 2 drew full-frame however the guest clipped it.
func TestClipWindowDisablesPixelsOutsideIt(t *testing.T) {
	l := New(newFakeBanks())
	l.SetResolution(0) // 256x192: X coordinates are 1:1

	// Clip to columns 10..20, rows 5..15.
	l.SetClip(10, 20, 5, 15)

	for _, tc := range []struct {
		x, y int
		want bool
	}{
		{15, 10, true},  // inside
		{10, 5, true},   // top-left corner, inclusive
		{20, 15, true},  // bottom-right corner, inclusive
		{9, 10, false},  // left of the window
		{21, 10, false}, // right of it
		{15, 4, false},  // above it
		{15, 16, false}, // below it
	} {
		_, enabled := l.fpgaSramAddr(tc.x, tc.y)
		if enabled != tc.want {
			t.Errorf("pixel (%d,%d): enabled = %v, want %v", tc.x, tc.y, enabled, tc.want)
		}
	}
}

// In 320x256 and 640x256 the X coordinates are in 2-pixel units:
// clip_x1_q <= i_clip_x1 & '0'; clip_x2_q <= i_clip_x2 & '1'.
func TestClipXIsInTwoPixelUnitsInWideModes(t *testing.T) {
	l := New(newFakeBanks())
	l.SetResolution(1) // 320x256
	l.SetClip(10, 20, 0, 255)

	for _, tc := range []struct {
		x    int
		want bool
	}{
		{19, false}, // just left of 10*2 = 20
		{20, true},  // 10*2
		{41, true},  // 20*2+1, the inclusive right edge
		{42, false},
	} {
		if _, enabled := l.fpgaSramAddr(tc.x, 10); enabled != tc.want {
			t.Errorf("x=%d: enabled = %v, want %v", tc.x, enabled, tc.want)
		}
	}
}

// The FPGA reset window is wide open, so an untouched Layer 2 must
// still draw everywhere.
func TestDefaultClipWindowIsWideOpen(t *testing.T) {
	l := New(newFakeBanks())
	l.SetResolution(0)
	for _, p := range [][2]int{{0, 0}, {255, 191}, {128, 96}} {
		if _, enabled := l.fpgaSramAddr(p[0], p[1]); !enabled {
			t.Errorf("pixel (%d,%d) disabled with no clip set", p[0], p[1])
		}
	}
}
