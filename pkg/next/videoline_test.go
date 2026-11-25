package next

import "testing"

// NR$1E/$1F read cvc (zxnext.vhd:5982-5986), the copper's vertical counter,
// not a line count from the frame interrupt. video/zxula_timing.vhd:455-466
// reloads cvc with NR$64 when vc = c_min_vactive, at hc = c_min_hactive - 12,
// and wraps it at c_max_vc. The frame interrupt fires at vc = c_int_v, hc =
// c_int_h (:551). For 128K timing at 50 Hz that is c_min_vactive 64,
// c_min_hactive 136 (so the reload at hc 124), c_max_vc 310, c_int_v 1 and
// c_int_h 128; at 60 Hz c_min_vactive 40, c_max_vc 263, c_int_v 0. hc counts
// two per T-state.
func TestActiveVideoLineIsCVC(t *testing.T) {
	for _, c := range []struct {
		name   string
		t      int // video T-states since the frame interrupt
		sixty  bool
		offset byte
		want   int
	}{
		{"at the interrupt", 0, false, 0, 248},           // vc 1: (1 - 64) mod 311
		{"just before the reload", 14361, false, 0, 310}, // vc 64, hc 122
		{"at the reload", 14362, false, 0, 0},            // vc 64, hc 124
		{"one line after the reload", 14362 + 228, false, 0, 1},
		{"line 255 is 7 lines after the interrupt", 1594, false, 0, 255},
		{"3 T before the interrupt (hc 122)", 70905, false, 0, 247},
		{"1 T before: cvc stepped at hc 124", 70907, false, 0, 248},
		{"NR$64 offsets the reload", 14362, false, 10, 10},
		{"NR$64 wraps at c_max_vc", 14362 + 301*228, false, 10, 0},
		{"60 Hz at the interrupt", 0, true, 0, 224}, // vc 0: (0 - 40) mod 264
		{"60 Hz at the reload", 9118, true, 0, 0},   // vc 40, hc 124
	} {
		if got := VideoTimingFor(0x02, c.sixty).ActiveVideoLine(c.t, c.offset); got != c.want {
			t.Errorf("%s: ActiveVideoLine(%d, %v, %d) = %d, want %d", c.name, c.t, c.sixty, c.offset, got, c.want)
		}
	}
}
