package next

import "testing"

// Frame-INT timing conformance (timing.md §1a/§1c, matrix A1-A4 + B1/B2).
// FrameIntTiming maps the NR$03 machine-timing (+ 50/60 Hz) to the maskable
// frame-INT assert point and the pulse width (CPU cycles). The frame origin is
// the interrupt itself, so the assert is 0 on every timing; the pulse is 32
// (48K/+3) or 36 (128K/Pentagon) (zxnext.vhd:2014-2033).
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
		{"48K 50Hz", mt48K, false, 0, 32},
		// A2 / B2
		{"128K 50Hz", mt128K, false, 0, 36},
		// A1 / B1  (NextZXOS boots in +3 timing)
		{"+3 50Hz", mtP3, false, 0, 32},
		// A4 / B2
		{"Pentagon", mtPent, false, 0, 36},
		// 60 Hz variants
		{"128K 60Hz", mt128K, true, 0, 36},
		{"+3 60Hz", mtP3, true, 0, 32},
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
	if a != 0 || p != 32 {
		t.Errorf("NR$03=$B3 → (assert=%d,pulse=%d), want (0,32)", a, p)
	}
}
