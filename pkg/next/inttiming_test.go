package next

import "testing"

// Frame-INT timing conformance (timing.md §1a/§1c, matrix A1-A4 + B1/B2).
// FrameIntTiming maps the NR$03 machine-timing (+ 50/60 Hz) to the maskable
// frame-INT assert point (wall-clock T-states from the ULA frame origin
// hc=0,vc=0) and the pulse width (CPU cycles). Values are derived directly
// from the FPGA: assert = (c_int_v·(c_max_hc+1) + c_int_h) / 2
// (zxula_timing.vhd:551,155-298), pulse = 32 (48K/+3) / 36 (128K/Pentagon)
// (zxnext.vhd:2014-2033).
func TestFrameIntTiming(t *testing.T) {
	const (
		mt48K  = 0x00 // 00X
		mt128K = 0x02 // 010
		mtP3   = 0x03 // 011
		mtPent = 0x04 // 100
	)
	cases := []struct {
		name       string
		mt         byte
		sixtyHz    bool
		wantAssert int
		wantPulse  int
	}{
		// A3 / B1
		{"48K 50Hz", mt48K, false, 58, 32}, // (0·448+116)/2, pulse 32
		// A2 / B2
		{"128K 50Hz", mt128K, false, 292, 36}, // (1·456+128)/2, pulse 36
		// A1 / B1  (NextZXOS boots in +3 timing)
		{"+3 50Hz", mtP3, false, 291, 32}, // (1·456+126)/2, pulse 32
		// A4 / B2
		{"Pentagon", mtPent, false, 71675, 36}, // (319·448+439)/2, pulse 36
		// 60 Hz variants: c_int_v=0 → assert = c_int_h/2
		{"128K 60Hz", mt128K, true, 64, 36}, // 128/2
		{"+3 60Hz", mtP3, true, 63, 32},     // 126/2
	}
	for _, c := range cases {
		gotA, gotP := FrameIntTiming(c.mt, c.sixtyHz)
		if gotA != c.wantAssert {
			t.Errorf("%s: assert = %d, want %d", c.name, gotA, c.wantAssert)
		}
		if gotP != c.wantPulse {
			t.Errorf("%s: pulse = %d, want %d", c.name, gotP, c.wantPulse)
		}
	}
}

// TestFrameIntTiming_MasksHighBits: only NR$03 bits 2:0 select timing.
func TestFrameIntTiming_MasksHighBits(t *testing.T) {
	// $B3 = +2A/+3 personality with timing bits 011 (+3).
	a, p := FrameIntTiming(0xB3, false)
	if a != 291 || p != 32 {
		t.Errorf("NR$03=$B3 → (assert=%d,pulse=%d), want (291,32)", a, p)
	}
}
