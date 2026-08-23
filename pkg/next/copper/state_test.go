package copper

import (
	"bytes"
	"encoding/gob"
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
		0x600B, // 2: MOVE NR$60,$0B — re-enters WriteData (see below)
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

	// Mid-operation: the cursor is moved to byte address 5 — an ODD
	// address, so the next NR$60 byte lands in the LOW half of word 2 —
	// and one byte is written there. That leaves the cursor at 6 and
	// turns word 2 into MOVE NR$60,$1A, which is what the drive sequence
	// below re-enters WriteData through. The odd address is the point:
	// the cursor's low bit is the whole of the half-word phase, and a
	// capture that loses it reassembles every later instruction half a
	// word out.
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
	// Word 2's low half carries the 0x1A the fixture wrote at byte 5.
	for i, want := range []uint16{0x1601, 0x80C8, 0x601A, 0x1702, 0x80D2, 0x0000, 0x1803, 0x80FA} {
		if got := c.Instruction(uint16(i)); got != Decode(want) {
			t.Errorf("instruction[%d] = %+v after restore, want %+v", i, got, Decode(want))
		}
	}
	if c.Cursor() != 6 {
		t.Errorf("cursor = %d after restore, want 6", c.Cursor())
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "next.copper" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.copper")
	}
}

// The replay tests above assert on behaviour, and behaviour is a filter: it
// only observes the fields that reach the transcript of NextReg writes. That
// filter is wide here because the fresh-copper replays start from New(), where
// every field differs from the capture — but it is a filter that a later change
// to the fixture could narrow without anything going red. Park the copper's
// program counter at 0 rather than 1, or capture at line 0, and the restore of
// that field stops being observable while every test still passes.
//
// So the tests below stop asserting on output and assert on the capture itself:
// drive the copper so EVERY captured field changes, restore, and re-capture. If
// any field is not restored, the second blob differs from the first. That
// observes all eight fields directly rather than the subset the write
// transcript happens to expose.

// driveEverything changes every field a capture carries, starting from primed().
//
// The pairing phase is the field to watch, and the reason the two WriteData
// calls at the end come in a pair rather than singly. primed() is captured with
// its high byte latched and hiSet true; a single write would latch a new high
// byte and leave hiSet true as well, so the phase would converge back to where
// it started and a lost restore of it would be invisible here. Completing the
// pair moves the latched byte AND leaves the phase false, so both differ.
//
// Order matters for the rest. The first mode write resets pc, so it has to
// precede the step that parks it; the cursor work has to follow the step,
// because SetWritePtrLow clears the very phase this is trying to move; and the
// stop comes last because it is a second mode write, which leaves the cursor's
// low byte — and so the phase — alone.
func driveEverything(c *Copper) {
	// A different list, ending in the $FFFF terminator so the copper parks.
	c.SetWritePtrLow(0)
	for _, w := range []uint16{0x2A05, 0x2B06, 0xFFFF} {
		c.WriteData(byte(w >> 8))
		c.WriteData(byte(w))
	}

	// A different start mode, running from the top of the new list.
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// One step retires both MOVEs and parks on the terminator, so pc lands at 2
	// (primed() left it at 1) and the raster lands on a line other than the
	// captured 40.
	c.Step(97, 511, 8)

	// The cursor and the pairing phase: a different index, a different latched
	// high byte, and the pair completed so the phase itself differs.
	c.SetWritePtrLow(9)
	c.WriteData(0x3C)
	c.WriteData(0x4D)

	// And stopped. The copper has no halt instruction: NR$62 mode 00 is its
	// only stop (device/copper.vhd:112-115), so the field is moved off
	// primed()'s false by writing that mode, which moves the mode with it.
	c.SetWritePtrHighAndMode(byte(StartStop) << 6)
}

func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	c := primed()
	want := c.SaveState()

	driveEverything(c)
	if bytes.Equal(c.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := c.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := c.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. If a later change retunes primed() or the
// drive sequence so a field stops differing between them, the round trip
// silently stops covering that field and the mutation score it earns becomes a
// number about the fixture rather than about the capture.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	c := primed()
	before := decodeStateForTest(t, c.SaveState())
	driveEverything(c)
	after := decodeStateForTest(t, c.SaveState())

	for _, f := range []struct {
		name string
		same bool
	}{
		{"Program", before.Program == after.Program},
		{"WritePtr", before.WritePtr == after.WritePtr},
		{"Mode", before.Mode == after.Mode},
		{"Pc", before.Pc == after.Pc},
		{"Stopped", before.Stopped == after.Stopped},
		{"LastScanline", before.LastScanline == after.LastScanline},
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

func decodeStateForTest(t *testing.T, b []byte) copperState {
	t.Helper()
	var s copperState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
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

// The write cursor changed meaning: it was a 10-bit instruction-word
// index and is now the FPGA's 11-bit byte address. gob does not police a
// schema — it decodes what it recognises and zero-fills the rest — so a
// capture written under the old meaning would silently restore at half
// the intended cursor and assemble every later instruction from the
// wrong offset. The wire form carries a version so such a capture is
// refused instead.
func TestLoadStateRefusesAnOlderWireFormat(t *testing.T) {
	c := primed()
	blob := c.SaveState()

	// Re-encode with the version field cleared, which is what an older
	// capture decodes to.
	var s copperState
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	s.Version = 0
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		t.Fatalf("encode: %v", err)
	}

	if err := New().LoadState(buf.Bytes()); err == nil {
		t.Error("LoadState accepted a state with no version: an older capture's cursor means something else")
	}
}

func TestSaveStateStampsTheCurrentVersion(t *testing.T) {
	var s copperState
	if err := gob.NewDecoder(bytes.NewReader(primed().SaveState())).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Version != copperStateVersion {
		t.Errorf("Version = %d, want %d", s.Version, copperStateVersion)
	}
}
