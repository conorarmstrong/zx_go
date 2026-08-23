package copper

import "testing"

// The copper's hcount_i is hc_ula (zxnext.vhd:3949 wired from o_hc_ula at
// zxnext.vhd:6737), whose zero point is 12 columns BEFORE the first displayed
// pixel: hc_ula is reset at c_min_hactive - 12 (video/zxula_timing.vhd:423-424)
// and zxula.vhd:44-46 states it outright — "0 corresponds to when the system is
// actually generating pixel 0 ... this position corresponds to ULA count
// i_hc = 0xC". So displayed pixel 0 IS hcount 12, and the WAIT threshold
// (X<<3)+12 (device/copper.vhd:94) means "release at displayed pixel 8X", not
// "release 12 pixels into displayed column X".

// TestHCountForPixelPlacesPixelZeroAtTwelve pins the origin itself.
func TestHCountForPixelPlacesPixelZeroAtTwelve(t *testing.T) {
	if got := HCountForPixel(0); got != 12 {
		t.Errorf("HCountForPixel(0) = %d, want 12 (zxula.vhd:46)", got)
	}
	if HCountOrigin != 12 {
		t.Errorf("HCountOrigin = %d, want 12", HCountOrigin)
	}
	if got := HCountForPixel(255); got != 267 {
		t.Errorf("HCountForPixel(255) = %d, want 267", got)
	}
}

// TestWaitReleasesAtDisplayedPixelEightX pins the consequence: a WAIT for
// column X releases exactly at displayed pixel 8X, for every column whose
// threshold fits the counter. Column 63 is the one that does not: see
// TestWaitColumn63WrapsToTheStartOfTheLine.
func TestWaitReleasesAtDisplayedPixelEightX(t *testing.T) {
	for x := 0; x < 63; x++ {
		want := HCountForPixel(x * 8)
		if got := WaitHThreshold(byte(x)); got != want {
			t.Errorf("WaitHThreshold(%d) = %d, want HCountForPixel(%d) = %d",
				x, got, x*8, want)
		}
	}
}

// TestWaitColumn63WrapsToTheStartOfTheLine pins the one column whose threshold
// does not fit the counter.
//
// device/copper.vhd:94 compares against
// `unsigned(copper_list_data_i(14 downto 9)&"000") + 12`. The left operand is
// NINE bits — a 6-bit column field concatenated with three zeros — and
// numeric_std's "+" returns a result the width of its left operand, so the add
// is 9-bit and wraps. Column 63 gives 63*8+12 = 516, which truncates to 4: the
// WAIT releases four columns into the line, not never. Column 62 is the
// largest that does not wrap (62*8+12 = 508).
//
// Proved under GHDL against the real device/copper.vhd
// (_tools/copper-vhdl-test, programs "wait-col63-wraps" / "wait-col62-parks"):
// WAIT x=63,v=7 emits at h=5 while the x=62 control emits nothing across the
// whole sweep.
func TestWaitColumn63WrapsToTheStartOfTheLine(t *testing.T) {
	if got := WaitHThreshold(62); got != 508 {
		t.Errorf("WaitHThreshold(62) = %d, want 508 (62*8+12, no wrap)", got)
	}
	if got := WaitHThreshold(63); got != 4 {
		t.Errorf("WaitHThreshold(63) = %d, want 4 (63*8+12 = 516, truncated to 9 bits)", got)
	}
}

// TestWaitColumn63ReleasesFourColumnsIntoTheLine drives the wrap through Step,
// so the arithmetic is pinned where it is used and not only where it is
// computed.
func TestWaitColumn63ReleasesFourColumnsIntoTheLine(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	wait := uint16(0x8000) | (uint16(63) << 9) | 7 // WAIT x=63, y=7
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait))
	c.WriteData(0x70) // MOVE reg $70
	c.WriteData(0x44) // val $44
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	for hc := uint16(0); hc < 4; hc++ {
		c.Step(7, hc, ClocksPerHCount)
	}
	if len(rw.writes) != 0 {
		t.Fatalf("WAIT x=63 released before hcount 4; writes = %+v", rw.writes)
	}
	c.Step(7, 4, ClocksPerHCount)
	if len(rw.writes) != 1 || rw.writes[0].reg != 0x70 || rw.writes[0].val != 0x44 {
		t.Errorf("WAIT x=63 did not release at hcount 4; writes = %+v", rw.writes)
	}
}
