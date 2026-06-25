package copper

import "testing"

// TestEndOfLineHcountReleasesScanlineWaits: stepping at the end-of-line hcount
// (>=511) releases a WAIT targeting any column on that scanline, so a
// per-scanline caller doesn't release such WAITs one scanline late (as hcount
// below the WAIT threshold would). The WAIT release threshold is
// hcount >= (X<<3)+12 (device/copper.vhd:94); for X=30 that is 252, so a
// mid-line hcount of 0 parks but the end-of-line hcount of 511 releases.
func TestEndOfLineHcountReleasesScanlineWaits(t *testing.T) {
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
	// Scanline 5, hcount 511 (end of line): threshold cleared → MOVE fires.
	c.Step(5, 511, 4)
	if len(rw.writes) != 1 || rw.writes[0].val != 0xAA {
		t.Errorf("hcount=511: WAIT(5,30) must release on scanline 5; writes=%v", rw.writes)
	}
}

// TestWaitHThresholdMatchesFPGA pins the exact horizontal release threshold
// formula taken from the FPGA copper: hcount >= (X<<3)+12.
func TestWaitHThresholdMatchesFPGA(t *testing.T) {
	cases := []struct {
		x    byte
		want uint16
	}{
		{0, 12}, {1, 20}, {2, 28}, {7, 68}, {30, 252}, {63, 516},
	}
	for _, tc := range cases {
		if got := WaitHThreshold(tc.x); got != tc.want {
			t.Errorf("WaitHThreshold(%d) = %d, want %d", tc.x, got, tc.want)
		}
	}
}
