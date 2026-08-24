package ula

import "testing"

// suspendTrackingLog is a raster journal that reports whether recording is
// currently suspended, so a test can ask what state the Copper's writes would
// have been recorded under.
type suspendTrackingLog struct {
	suspended bool
	replaying bool
}

func (l *suspendTrackingLog) BeginReplay()     { l.replaying = true }
func (l *suspendTrackingLog) ApplyThrough(int) {}
func (l *suspendTrackingLog) EndReplay()       { l.replaying = false }
func (l *suspendTrackingLog) Len() int         { return 0 }
func (l *suspendTrackingLog) SuspendRecording() func() {
	was := l.suspended
	l.suspended = true
	return func() { l.suspended = was }
}

// recording reports whether a NextReg write made right now would go into the
// journal: rasterlog.Record drops the entry while either the replay window or
// an explicit suspension is open.
func (l *suspendTrackingLog) recording() bool { return !l.replaying && !l.suspended }

// journalProbeCopper asks the journal, at every clock it is given, whether a
// write it made there would be recorded.
type journalProbeCopper struct {
	log            *suspendTrackingLog
	recordableRows []int
}

func (c *journalProbeCopper) Step(scanline, _ uint16, _ int) int {
	if c.log.recording() {
		c.recordableRows = append(c.recordableRows, int(scanline))
	}
	return 0
}
func (c *journalProbeCopper) Wrote() bool { return false }
func (c *journalProbeCopper) Idle() bool  { return false }

// The raster journal exists to replay the GUEST's mid-frame writes. The
// Copper's own writes must never enter it: they land at the right row by
// construction, and a journalled one is undone by the next frame's BeginReplay
// and only redone after every row has been composed, so a MOVE made on a border
// line would mis-colour the whole following frame.
//
// BeginReplay's window covers the displayed rows for free. The Copper also runs
// below the last display row, outside that window, and that loop has to ask for
// the same protection. It did not until v1.12.0, and the fix shipped without a
// test at this level: deleting the SuspendRecording call broke nothing.
func TestCopperWritesAreNeverJournalled(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	log := &suspendTrackingLog{}
	c := &journalProbeCopper{log: log}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})
	u.SetNextRasterLog(log)

	u.applyNextCompositor()

	if len(c.recordableRows) != 0 {
		t.Errorf("the Copper was clocked on %d row(s) where its writes would have "+
			"been journalled, first %d: every Copper clock must fall inside either "+
			"the replay window or an explicit suspension",
			len(c.recordableRows), c.recordableRows[0])
	}
}
