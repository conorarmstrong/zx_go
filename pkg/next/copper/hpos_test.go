package copper

import "testing"

// TestEndOfLineHposReleasesScanlineWaits: stepping at end-of-line hpos
// (63) releases a WAIT targeting any hpos on that scanline, so a
// per-scanline caller doesn't release such WAITs one scanline late (as
// hpos=0 would).
func TestEndOfLineHposReleasesScanlineWaits(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	wait := uint16(0x8000) | (uint16(30) << 9) | 5 // WAIT Y=5, X=30
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait & 0xFF))
	c.WriteData(0x10) // MOVE reg 0x10
	c.WriteData(0xAA) // val 0xAA
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// Scanline 5, hpos 0: WAIT(5,30) not satisfied → MOVE parked.
	c.Step(5, 0, 4)
	if len(rw.writes) != 0 {
		t.Fatalf("hpos=0: WAIT(5,30) must not release on scanline 5; writes=%v", rw.writes)
	}
	// Scanline 5, hpos 63 (end of line): WAIT releases → MOVE fires.
	c.Step(5, 63, 4)
	if len(rw.writes) != 1 || rw.writes[0].val != 0xAA {
		t.Errorf("hpos=63: WAIT(5,30) must release on scanline 5; writes=%v", rw.writes)
	}
}
