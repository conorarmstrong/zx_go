package rasterlog

import "testing"

// The Spectrum Next layers are composited at the END of a frame, reading
// whatever the NextRegs hold at that moment. A CPU write partway down the
// screen therefore applied retroactively to the whole frame, so raster splits
// driven from the CPU (rather than from the Copper, which is stepped inside
// the row loop) did not reproduce at all.
//
// A Log records each visual change against the display row it happened on.
// The compositor rewinds to the frame-start state, then walks down applying
// each change as it reaches its row.

// newTestLog returns a Log whose row source the test drives directly.
func newTestLog(row *int) *Log {
	return New(func() int { return *row })
}

// TestReplayAppliesChangesAtTheirRow is the core behaviour: a value written
// at row 100 must be the old one for rows above it and the new one below.
func TestReplayAppliesChangesAtTheirRow(t *testing.T) {
	var v int
	row := 0

	log := newTestLog(&row)
	v = 1
	row = 100
	log.Record(func() { v = 1 }, func() { v = 5 })
	v = 5 // the live write the guest actually made

	log.BeginReplay()
	if v != 1 {
		t.Fatalf("after Rewind v = %d, want the frame-start 1", v)
	}
	for r := 0; r < 192; r++ {
		log.ApplyThrough(r)
		want := 1
		if r >= 100 {
			want = 5
		}
		if v != want {
			t.Fatalf("row %d: v = %d, want %d", r, v, want)
		}
	}
}

// TestReplayLeavesTheLiveStateAtFrameEnd pins that the registers finish where
// the guest left them, including writes made after the last display row —
// otherwise the next frame would start from a stale value.
func TestReplayLeavesTheLiveStateAtFrameEnd(t *testing.T) {
	var v int
	row := 0

	log := newTestLog(&row)
	v = 1

	row = 50
	log.Record(func() { v = 1 }, func() { v = 2 })
	v = 2
	// A write during the bottom border, after the last display row.
	row = RowAfterDisplay
	log.Record(func() { v = 2 }, func() { v = 9 })
	v = 9

	log.BeginReplay()
	for r := 0; r < 192; r++ {
		log.ApplyThrough(r)
	}
	if v != 2 {
		t.Errorf("last display row: v = %d, want 2 (the bottom-border write is not on screen)", v)
	}
	log.EndReplay()
	if v != 9 {
		t.Errorf("after ApplyAll: v = %d, want the live 9", v)
	}
}

// TestWritesBeforeTheDisplayApplyFromTheTop pins that a change made during
// the top border or vblank is in force for row 0.
func TestWritesBeforeTheDisplayApplyFromTheTop(t *testing.T) {
	var v int
	row := RowBeforeDisplay

	log := newTestLog(&row)
	v = 1
	log.Record(func() { v = 1 }, func() { v = 7 })
	v = 7

	log.BeginReplay()
	log.ApplyThrough(0)
	if v != 7 {
		t.Errorf("row 0: v = %d, want 7 (written before the display started)", v)
	}
}

// TestMultipleWritesToOneValueReplayInOrder pins that the LAST write at or
// above a row wins, and that rewinding several writes restores the value from
// before the first of them.
func TestMultipleWritesToOneValueReplayInOrder(t *testing.T) {
	var v int
	row := 0

	log := newTestLog(&row)
	v = 0
	for i, r := range []int{10, 20, 30} {
		old, nw := i, i+1
		row = r
		log.Record(func() { v = old }, func() { v = nw })
		v = nw
	}

	log.BeginReplay()
	if v != 0 {
		t.Fatalf("after Rewind v = %d, want 0", v)
	}
	for _, tc := range []struct{ row, want int }{
		{0, 0}, {9, 0}, {10, 1}, {19, 1}, {20, 2}, {29, 2}, {30, 3}, {191, 3},
	} {
		log.BeginReplay()
		for r := 0; r <= tc.row; r++ {
			log.ApplyThrough(r)
		}
		if v != tc.want {
			t.Errorf("row %d: v = %d, want %d", tc.row, v, tc.want)
		}
	}
}

// TestResetClearsTheFrame pins that a Log does not leak entries between
// frames.
func TestResetClearsTheFrame(t *testing.T) {
	var v int
	row := 40
	log := newTestLog(&row)
	log.Record(func() { v = 1 }, func() { v = 2 })
	if log.Len() != 1 {
		t.Fatalf("Len = %d, want 1", log.Len())
	}
	log.EndReplay()
	if log.Len() != 0 {
		t.Errorf("Len after Reset = %d, want 0", log.Len())
	}
	v = 3
	log.BeginReplay()
	if v != 3 {
		t.Errorf("Rewind on an empty log changed v to %d", v)
	}
}

// TestEmptyLogIsFree guards the hot path: with no journalled writes the
// replay must not touch anything.
func TestEmptyLogIsFree(t *testing.T) {
	row := 0
	log := newTestLog(&row)
	log.BeginReplay()
	for r := 0; r < 192; r++ {
		log.ApplyThrough(r)
	}
	log.EndReplay()
	if log.Len() != 0 {
		t.Errorf("Len = %d, want 0", log.Len())
	}
}

// TestNilLogIsSafe pins that an unwired journal is a no-op, so the classic
// models and any Next path without one behave exactly as before.
func TestNilLogIsSafe(t *testing.T) {
	var log *Log
	log.Record(func() {}, func() {})
	log.BeginReplay()
	log.ApplyThrough(0)
	log.EndReplay()
	log.EndReplay()
	if log.Len() != 0 {
		t.Errorf("nil log Len = %d, want 0", log.Len())
	}
}

// TestRecordIsIgnoredWhileReplaying guards against the obvious way this
// blows up: an undo/redo closure calls the same setter it was recorded from,
// the setter's observer records again, and the log grows without bound (or
// recurses) every frame. Replay is not guest activity, so it must not be
// journalled.
func TestRecordIsIgnoredWhileReplaying(t *testing.T) {
	var v int
	row := 10
	log := newTestLog(&row)

	// A "setter" that records every change, exactly as an observer would.
	var set func(n int)
	set = func(n int) {
		old := v
		log.Record(func() { set(old) }, func() { set(n) })
		v = n
	}

	set(1)
	set(2)
	if log.Len() != 2 {
		t.Fatalf("Len = %d, want 2", log.Len())
	}

	log.BeginReplay()
	for r := 0; r < 192; r++ {
		log.ApplyThrough(r)
	}
	// Checked inside the window: EndReplay clears the frame, so growth has to
	// be caught before then.
	if log.Len() != 2 {
		t.Errorf("Len during the replay = %d, want 2 (replay must not journal itself)", log.Len())
	}
	log.EndReplay()

	if v != 2 {
		t.Errorf("v = %d, want 2", v)
	}
	if log.Len() != 0 {
		t.Errorf("Len after EndReplay = %d, want 0", log.Len())
	}
}

// TestReplayingReportsTheReplayWindow lets observers skip other bookkeeping
// while the compositor is walking the frame back and forth.
func TestReplayingReportsTheReplayWindow(t *testing.T) {
	row := 5
	log := newTestLog(&row)

	var duringUndo, duringRedo bool
	log.Record(
		func() { duringUndo = log.Replaying() },
		func() { duringRedo = log.Replaying() },
	)

	if log.Replaying() {
		t.Error("Replaying() is true outside a replay")
	}
	log.BeginReplay()
	log.EndReplay()
	if !duringUndo {
		t.Error("Replaying() was false inside undo")
	}
	if !duringRedo {
		t.Error("Replaying() was false inside redo")
	}
	if log.Replaying() {
		t.Error("Replaying() stayed true after the replay")
	}
}

// TestNothingIsRecordedDuringTheWholeReplayWindow pins the wider suppression.
// The compositor steps the Copper inside its row loop, so the Copper writes
// NextRegs BETWEEN ApplyThrough calls, not inside a redo closure. Those writes
// already land at the right row by construction; journalling them would grow
// the log by thousands of entries a frame on a Copper-heavy program.
func TestNothingIsRecordedDuringTheWholeReplayWindow(t *testing.T) {
	row := 0
	log := newTestLog(&row)

	row = 20
	log.Record(func() {}, func() {})
	if log.Len() != 1 {
		t.Fatalf("Len = %d, want 1", log.Len())
	}

	log.BeginReplay()
	for r := 0; r < 192; r++ {
		log.ApplyThrough(r)
		// Stands in for a Copper MOVE landing between rows.
		log.Record(func() {}, func() {})
		if !log.Replaying() {
			t.Fatalf("row %d: the replay window closed early", r)
		}
	}
	if log.Len() != 1 {
		t.Errorf("Len during the window = %d, want 1 (Copper writes must not be journalled)", log.Len())
	}

	log.EndReplay()
	if log.Len() != 0 {
		t.Errorf("Len after EndReplay = %d, want 0", log.Len())
	}
	if log.Replaying() {
		t.Error("the replay window stayed open after EndReplay")
	}
	// Recording resumes for the next frame.
	row = 5
	log.Record(func() {}, func() {})
	if log.Len() != 1 {
		t.Errorf("Len after the window = %d, want 1", log.Len())
	}
}

// TestJournalDoesNotAccumulateAcrossUnrenderedFrames pins the bug that
// shipped in v1.6.0: the log was only ever cleared by EndReplay, which the
// compositor calls. Any path that executes frames WITHOUT rendering — headless
// stepping, fast tape turbo, the debugger, RZX playback — left entries piling
// up forever. Measured on a real Next boot it reached 113,766 entries and was
// still climbing, which is both a memory leak and a correctness bug: the next
// rewind would undo state from thousands of frames earlier.
//
// A frame that is never rendered has no journal worth keeping — its writes are
// already live — so the log discards them as soon as the frame changes.
func TestJournalDoesNotAccumulateAcrossUnrenderedFrames(t *testing.T) {
	row := 10
	frame := uint64(0)
	log := NewWithFrame(func() int { return row }, func() uint64 { return frame })

	for f := 0; f < 500; f++ {
		frame = uint64(f)
		// Several writes per frame, and no render at all.
		for i := 0; i < 20; i++ {
			log.Record(func() {}, func() {})
		}
		if log.Len() > 20 {
			t.Fatalf("frame %d: Len = %d, want at most one frame's worth (20)", f, log.Len())
		}
	}
}

// TestJournalKeepsTheCurrentFrameIntact guards the other side: entries within
// a single frame must all survive, or the replay is incomplete.
func TestJournalKeepsTheCurrentFrameIntact(t *testing.T) {
	row := 5
	frame := uint64(7)
	log := NewWithFrame(func() int { return row }, func() uint64 { return frame })

	for i := 0; i < 50; i++ {
		log.Record(func() {}, func() {})
	}
	if log.Len() != 50 {
		t.Errorf("Len = %d, want 50 (all of one frame's writes)", log.Len())
	}
}

// TestJournalIsBoundedWithinAFrame is the backstop: even a pathological frame
// writing without limit must not grow the log without limit.
func TestJournalIsBoundedWithinAFrame(t *testing.T) {
	row := 0
	frame := uint64(1)
	log := NewWithFrame(func() int { return row }, func() uint64 { return frame })

	for i := 0; i < MaxEntries*3; i++ {
		log.Record(func() {}, func() {})
	}
	if log.Len() > MaxEntries {
		t.Errorf("Len = %d, want at most MaxEntries (%d)", log.Len(), MaxEntries)
	}
}

// Not everything that writes a NextReg is a guest write. The Copper writes
// registers on the machine's own behalf, and BeginReplay's window exists partly
// to keep those out of the log: they land at the right row by construction, and
// journalling them would both grow the log without bound and, worse, let a
// later replay undo them.
//
// Work that happens outside a replay window needs the same protection, which is
// what this is for. It is not a replay: nothing is rewound and nothing is
// re-applied.
func TestSuspendRecordingKeepsNonGuestWritesOutOfTheLog(t *testing.T) {
	l := New(func() int { return 100 })

	l.Record(func() {}, func() {})
	if got := l.Len(); got != 1 {
		t.Fatalf("entries after an ordinary write = %d, want 1", got)
	}

	resume := l.SuspendRecording()
	l.Record(func() {}, func() {})
	l.Record(func() {}, func() {})
	if got := l.Len(); got != 1 {
		t.Errorf("entries while suspended = %d, want 1: writes made on the "+
			"machine's behalf must not be journalled", got)
	}
	resume()

	l.Record(func() {}, func() {})
	if got := l.Len(); got != 2 {
		t.Errorf("entries after resuming = %d, want 2: recording must come back", got)
	}
}

// Suspending must not disturb a replay that is already in progress: resuming
// has to leave the window as it found it, or the rest of the frame starts
// journalling its own replay.
func TestSuspendRecordingNestsInsideAReplayWindow(t *testing.T) {
	l := New(func() int { return 100 })
	l.BeginReplay()
	if !l.Replaying() {
		t.Fatal("BeginReplay did not open the window")
	}
	resume := l.SuspendRecording()
	resume()
	if !l.Replaying() {
		t.Error("resuming closed a replay window it did not open")
	}
}
