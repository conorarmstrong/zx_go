package copper

import "testing"

// TestWaitReleasesOnlyPastItsColumnThreshold: on the WAIT's own scanline the
// horizontal gate alone decides, and it is hcount >= (X<<3)+12
// (device/copper.vhd:94). For X=30 that is 252, so an hcount of 0 parks the
// list and any hcount at or past 252 releases it.
func TestWaitReleasesOnlyPastItsColumnThreshold(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	wait := uint16(0x8000) | (uint16(30) << 9) | 5 // WAIT Y=5, X=30 (col)
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait & 0xFF))
	c.WriteData(0x10) // MOVE reg 0x10
	c.WriteData(0xAA) // val 0xAA
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// Scanline 5, hcount 0: WAIT(5,30) threshold 252 not reached → MOVE parked.
	c.Step(5, 0, 4)
	if len(rw.writes) != 0 {
		t.Fatalf("hcount=0: WAIT(5,30) must not release on scanline 5; writes=%v", rw.writes)
	}
	// One column short of the threshold: still parked.
	c.Step(5, 251, 4)
	if len(rw.writes) != 0 {
		t.Fatalf("hcount=251: WAIT(5,30) must not release below its threshold; writes=%v", rw.writes)
	}
	// Scanline 5, hcount 252: threshold cleared → MOVE fires.
	c.Step(5, 252, 4)
	if len(rw.writes) != 1 || rw.writes[0].val != 0xAA {
		t.Errorf("hcount=252: WAIT(5,30) must release on scanline 5; writes=%v", rw.writes)
	}
}

// TestWaitHThresholdMatchesFPGA pins the exact horizontal release threshold
// formula taken from the FPGA copper: hcount >= (X<<3)+12, computed in the
// nine bits copper.vhd:94 computes it in, so column 63 wraps to 4.
func TestWaitHThresholdMatchesFPGA(t *testing.T) {
	cases := []struct {
		x    byte
		want uint16
	}{
		{0, 12}, {1, 20}, {2, 28}, {7, 68}, {30, 252}, {62, 508}, {63, 4},
	}
	for _, tc := range cases {
		if got := WaitHThreshold(tc.x); got != tc.want {
			t.Errorf("WaitHThreshold(%d) = %d, want %d", tc.x, got, tc.want)
		}
	}
}
