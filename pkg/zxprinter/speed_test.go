package zxprinter

import "testing"

// A mid-print OUT with a changed speed bit (0x02) must not retime the
// line already in progress: the drum is mechanical, so a speed change
// only takes effect at the next line start. While the drum position is
// still in the pre-line sync window (x<0) there is no in-progress line
// to protect, so the change applies immediately instead of being
// deferred.

// TestSpeedChangeInSyncWindowAppliesImmediately covers a speed-change
// write that lands before the visible line has started (drum position
// x<0): the new speed takes effect right away, with nothing deferred.
func TestSpeedChangeInSyncWindowAppliesImmediately(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x00, 0) // motor on, fast (bit1=0), stylus off
	if p.speed != 2 {
		t.Fatalf("setup: speed = %d, want 2 (fast)", p.speed)
	}
	// x = cycles/cpp - 64; at fast speed cpp=220, cycles=63*220 gives x=-1.
	tstates := int64(63 * 220)
	p.HandlePortWrite(0xFB, 0x02, tstates) // request slow (bit1=1)
	if p.speed != 1 {
		t.Errorf("speed in sync window = %d, want 1 (applied immediately)", p.speed)
	}
	if p.newSpeed != 0 {
		t.Errorf("newSpeed = %d, want 0 (nothing deferred)", p.newSpeed)
	}
}

// TestSpeedChangeMidLineIsDeferred covers a speed-change write that
// lands after the line has started (drum position x>=0): the running
// line keeps its original speed and the change is recorded as pending.
func TestSpeedChangeMidLineIsDeferred(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x00, 0) // motor on, fast, stylus off
	// x = 10: cycles = (10+64)*220.
	tstates := int64((10 + 64) * 220)
	p.HandlePortWrite(0xFB, 0x02, tstates) // request slow mid-line
	if p.speed != 2 {
		t.Errorf("speed changed mid-line: got %d, want still 2 (deferred)", p.speed)
	}
	if p.newSpeed != 1 {
		t.Errorf("newSpeed = %d, want 1 (pending slow-speed change)", p.newSpeed)
	}
}

// TestSpeedChangeAppliesAtNextLineWrap covers the deferred change from
// TestSpeedChangeMidLineIsDeferred actually taking effect once the
// drum wraps into the next line.
func TestSpeedChangeAppliesAtNextLineWrap(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x00, 0) // motor on, fast, stylus off
	tstates := int64((10 + 64) * 220)
	p.HandlePortWrite(0xFB, 0x02, tstates) // request slow mid-line (deferred)

	// Drive the drum past the end of the first line (x>=320 at the
	// still-fast speed: cycles=(320+64)*220) via a read, which also
	// advances the drum.
	_, _ = p.HandlePortRead(0xFB, (320+64)*220+1)
	if p.speed != 1 {
		t.Errorf("speed after line wrap = %d, want 1 (deferred change applied)", p.speed)
	}
	if p.newSpeed != 0 {
		t.Errorf("newSpeed after applying = %d, want 0", p.newSpeed)
	}
}

// TestSpeedChangeToSameSpeedClearsPending covers requesting the
// current speed again after a pending change was recorded: it cancels
// the pending change rather than leaving a stale one queued.
func TestSpeedChangeToSameSpeedClearsPending(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x00, 0) // motor on, fast, stylus off
	tstates := int64((10 + 64) * 220)
	p.HandlePortWrite(0xFB, 0x02, tstates) // request slow mid-line (deferred)
	if p.newSpeed != 1 {
		t.Fatalf("setup: newSpeed = %d, want 1", p.newSpeed)
	}
	// Same line, request fast again (back to the running speed).
	p.HandlePortWrite(0xFB, 0x00, tstates+1)
	if p.newSpeed != 0 {
		t.Errorf("newSpeed = %d, want 0 (re-requesting the running speed cancels the pending change)", p.newSpeed)
	}
}
