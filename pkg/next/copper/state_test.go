package copper

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*Copper)(nil)

// The copper is the device that proves a rewind needs more than the list.
//
// Two kilobytes of instructions are the bulk of the bytes but not the part that
// decides what happens next. That comes from the small state around them: where
// the program counter is parked inside the list, whether the copper is running
// at all and in which start mode, the raster line the last step was taken at
// (which is what a StartOnVBL restart compares against), the write cursor, and
// — the one with teeth — the NR$60 hi/lo byte-pairing phase.
//
// NR$60 delivers a 16-bit instruction as two consecutive register writes: the
// first is latched, the second commits the pair. Capture between them and
// restore without the phase, and the guest's next byte is taken as a high half
// when it was meant as a low one; every instruction after it is off by one, so
// a real "WAIT y; MOVE r,v" list resumes as garbage MOVEs that clobber the
// whole NextReg config. Not resetting that phase on a cursor set was a real bug
// in this emulator (see SetWritePtrLow); losing it across a rewind is the same
// failure one step along.
//
// These tests are a replay property, not a field-by-field comparison, because a
// field-by-field test only checks the fields someone remembered to add.

// primed returns a Copper part way through several things at once: a list
// uploaded and running, the program counter parked inside it on an unsatisfied
// WAIT, the raster already 40 lines into the frame, the write cursor moved back
// into the middle of the list, and an NR$60 pair with its high byte latched and
// its low byte not yet written.
//
// The half-written pair is the point of the fixture. Its low byte does not
// arrive from the test: it arrives from the list itself, from the MOVE NR$60 at
// index 2, so the copper completes the pair while it is running and writes the
// instruction at index 5 that it is about to execute. Resume with the pairing
// phase, the cursor, the latched byte or the list wrong and index 5 executes as
// something else.
func primed() *Copper {
	c := New()

	prog := []uint16{
		0x1601, // 0: MOVE NR$16,$01
		0x80C8, // 1: WAIT y=200 — where the fixture is parked
		0x600B, // 2: MOVE NR$60,$0B — re-enters WriteData, completing the pair
		0x1702, // 3: MOVE NR$17,$02
		0x80D2, // 4: WAIT y=210
		0x0000, // 5: NOOP until index 2 writes MOVE NR$1A,$0B over it
		0x1803, // 6: MOVE NR$18,$03
		0x80FA, // 7: WAIT y=250 — parks at the foot of the frame, still running
	}
	c.SetWritePtrLow(0)
	for _, w := range prog {
		c.WriteData(byte(w >> 8))
		c.WriteData(byte(w))
	}
	// Run from index 0, restarting at every VBL.
	c.SetWritePtrHighAndMode(byte(StartOnVBL) << 6)

	// Drive the raster to line 40 with no RegWriter installed, so the MOVE at
	// index 0 retires silently and the copper parks on the WAIT at index 1.
	// That leaves pc inside the list and lastScanline at 40 — far enough down
	// the frame that a step at line 45 is NOT a VBL wrap and a step at line 20
	// is.
	c.Step(40, 511, 8)

	// Mid-operation: cursor moved back to index 5, high half of an NR$60 pair
	// latched, low half still to come.
	c.SetWritePtrLow(5)
	c.WriteData(0x1A)

	return c
}

// recorder is the RegWriter the drive installs. It records every MOVE the
// copper executes together with the raster line it executed on, and forwards
// NR$60 back into the copper exactly as the real NextReg dispatcher does
// (WireCopper, pkg/next/wire.go), so a MOVE into the copper's own data port
// really does write the list.
type recorder struct {
	c    *Copper
	line uint16
	log  []string
}

func (r *recorder) WriteReg(reg, val byte) {
	r.log = append(r.log, fmt.Sprintf("line=%d MOVE $%02X,$%02X", r.line, reg, val))
	if reg == 0x60 {
		r.c.WriteData(val)
	}
}

// drive runs a fixed raster script and returns a transcript of the only thing
// the copper produces: the stream of NextReg writes, and which line each one
// landed on.
//
// Two frames, because one frame cannot show a StartOnVBL restart. The first
// starts at firstLine, the second wraps back to the top of the frame.
func drive(c *Copper, firstLine uint16) []byte {
	r := &recorder{c: c}
	c.SetRegWriter(r)

	for frame := 0; frame < 2; frame++ {
		start := firstLine
		if frame > 0 {
			start = 5
		}
		for line := start; line <= 250; line++ {
			r.line = line
			c.Step(line, 511, 4)
		}
	}
	return []byte(strings.Join(r.log, "\n"))
}

// firstLineNoWrap is above the fixture's last stepped line, so resuming from
// the capture is a continuation rather than a VBL restart. That is what makes
// the parked program counter observable: a restart would reset it to 0 and
// hide whether it was captured at all.
const firstLineNoWrap = 45

// The property that matters: from a captured state, the copper must execute the
// instructions it was going to execute, on the lines it was going to execute
// them.
func TestReplayingFromCapturedStateReproducesTheSameWrites(t *testing.T) {
	c := primed()

	st := c.SaveState()
	first := drive(c, firstLineNoWrap)

	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(c, firstLineNoWrap)

	if !bytes.Equal(first, second) {
		t.Errorf("the copper executed a different program on replay from the same captured state:\n"+
			"first run:\n%s\n\nsecond run:\n%s", first, second)
	}
}

// The same property against a copper that has never seen the list. A same
// instance replay cannot tell whether the instruction memory was captured — the
// copper still holds it either way — so this is the test that makes the list,
// the start mode and the halted flag load-bearing.
func TestRestoringIntoAFreshCopperResumesIdentically(t *testing.T) {
	c := primed()

	st := c.SaveState()
	want := drive(c, firstLineNoWrap)

	fresh := New()
	if err := fresh.LoadState(st); err != nil {
		t.Fatalf("LoadState into a fresh copper: %v", err)
	}
	got := drive(fresh, firstLineNoWrap)

	if !bytes.Equal(want, got) {
		t.Errorf("a fresh copper restored from the capture executed a different program:\n"+
			"captured machine:\n%s\n\nrestored machine:\n%s", want, got)
	}
}

// The negative that gives the tests above their teeth. Uploading the same list
// and starting it the same way — everything a NextReg-level replay could
// reconstruct — does not resume the machine, because it loses where the program
// counter was parked, where the raster was, and the half-written NR$60 pair.
func TestReuploadingTheListIsNotEnoughToResume(t *testing.T) {
	c := primed()
	st := c.SaveState()
	want := drive(c, firstLineNoWrap)

	// Rebuild from the guest-visible writes alone: the list as it was uploaded,
	// then the same start mode.
	naive := New()
	naive.SetWritePtrLow(0)
	for _, w := range []uint16{0x1601, 0x80C8, 0x600B, 0x1702, 0x80D2, 0x0000, 0x1803, 0x80FA} {
		naive.WriteData(byte(w >> 8))
		naive.WriteData(byte(w))
	}
	naive.SetWritePtrHighAndMode(byte(StartOnVBL) << 6)
	if got := drive(naive, firstLineNoWrap); bytes.Equal(want, got) {
		t.Fatal("the list and the start mode alone reproduced the run, so this fixture " +
			"is not mid-operation and the replay tests above are not measuring anything")
	}

	// ...and the full capture does what re-uploading could not.
	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := drive(c, firstLineNoWrap); !bytes.Equal(want, got) {
		t.Error("full state capture failed to reproduce the run it was taken from")
	}
}

// The VBL restart compares the line being stepped against the line last
// stepped, so that comparison has a stored half. Resuming BELOW the captured
// line must restart the list exactly as it would have without the rewind.
//
// Restoring into a fresh copper is what makes the stored half load-bearing: it
// starts at line 0, so a capture that dropped it sees the resume as a step
// forward through a continuous frame, never restarts, and runs a frame out of
// phase with the raster it is painting.
func TestVBLRestartComparisonSurvivesCapture(t *testing.T) {
	const belowCapturedLine = 20 // the fixture last stepped line 40

	c := primed()
	st := c.SaveState()
	want := drive(c, belowCapturedLine)

	fresh := New()
	if err := fresh.LoadState(st); err != nil {
		t.Fatalf("LoadState into a fresh copper: %v", err)
	}
	if got := drive(fresh, belowCapturedLine); !bytes.Equal(want, got) {
		t.Errorf("resuming across the top of the frame diverged:\ncaptured:\n%s\n\nrestored:\n%s",
			want, got)
	}
}

// A capture must not alias the live copper, and must not be rewritten by a
// copper that carries on running. The registry copies the blob too, but a
// device handing back a view of its own memory is a bug worth catching here.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	c := primed()

	st := c.SaveState()
	snapshot := append([]byte(nil), st...)

	// Carry on: overwrite the list and run it somewhere else entirely.
	c.SetWritePtrLow(0)
	for i := 0; i < 8; i++ {
		c.WriteData(0x33)
		c.WriteData(byte(i))
	}
	drive(c, firstLineNoWrap)

	if !bytes.Equal(st, snapshot) {
		t.Error("the captured blob changed while the copper kept running: SaveState handed back live memory")
	}
	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i, want := range []uint16{0x1601, 0x80C8, 0x600B, 0x1702, 0x80D2, 0x0000, 0x1803, 0x80FA} {
		if got := c.Instruction(uint16(i)); got != Decode(want) {
			t.Errorf("instruction[%d] = %+v after restore, want %+v", i, got, Decode(want))
		}
	}
	if c.Cursor() != 5 {
		t.Errorf("cursor = %d after restore, want 5", c.Cursor())
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "next.copper" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.copper")
	}
}

// A malformed blob must be reported and must change nothing. A copper left with
// its list restored and its pairing phase in the present is exactly the
// corruption this file exists to prevent.
func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"rubbish", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"truncated", primed().SaveState()[:12]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := primed()
			if err := c.LoadState(tc.blob); err == nil {
				t.Fatal("a malformed state blob must be reported, not half-applied")
			}
			// Whatever it rejected, it must have kept the machine it had: an
			// untouched copper driven the same way produces the same run.
			if got, want := drive(c, firstLineNoWrap), drive(primed(), firstLineNoWrap); !bytes.Equal(got, want) {
				t.Errorf("the copper was disturbed by a state it rejected:\nafter reject:\n%s\n\nuntouched:\n%s",
					got, want)
			}
		})
	}
}
