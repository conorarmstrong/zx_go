package ula

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*TapePlayer)(nil)

// The tape player is the device where "restore the registers" is most obviously
// not enough, because it has no registers. What decides the next edge is
// entirely position: which block, how far into that block's pulse train, when
// the last edge fired, and which way the EAR line was left. None of it is
// visible to the guest, and none of it is in any snapshot format.
//
// These tests assert on the CAPTURE rather than on behaviour. A behavioural
// assertion only reaches the fields playback happens to expose, and several of
// these do not reach it at all: dataConsumed is read by the fast-load trap and
// never by the pulse loop, and an open TZX loop frame only shows up blocks
// later. There is one replay test below as well, because the blob comparison
// cannot see the derived pulse train that LoadState rebuilds.

// stateTestTape builds a tape whose layout can move every captured field: two
// data blocks of different sizes with a counted TZX loop between them, so the
// drive sequence crosses a block boundary and opens a loop frame.
func stateTestTape(t *testing.T) *TapePlayer {
	t.Helper()
	b := newTZXBuilder()
	b.standardBlock([]byte{0xFF, 0x01, 0x02, 0x03, 0x04, 0x05}) // 0: data block
	b.raw(0x24, 0x03, 0x00)                                     // 1: loop start, 3 iterations
	b.standardBlock([]byte{0xFF, 0x10, 0x11})                   // 2: a shorter block, inside the loop
	b.raw(0x25)                                                 // 3: loop end
	b.standardBlock([]byte{0xFF, 0x20})                         // 4: after the loop
	return loadTZXBytes(t, b)
}

// primedTape returns a player part-way into the first block's pilot tone: mid
// pulse train, with an edge already behind it and the tape running. A capture
// taken at a block boundary would prove nothing about position.
func primedTape(t *testing.T) *TapePlayer {
	t.Helper()
	tp := stateTestTape(t)
	tp.Play()
	// An odd number of steps, and a step that is not a whole number of pilot
	// pulses, so the player stops between edges rather than on one.
	for i := 0; i < 7; i++ {
		tp.Update(5000)
	}
	return tp
}

// driveTapeEverything moves every field a capture carries.
//
// It plays on until the tape is sitting in the trailing pause of the block
// INSIDE the loop, which is the one position that moves all of them at once:
// the block index and the pulse-train identity change, the pulse index lands
// somewhere unrelated, dataConsumed goes true (the block's bytes are off the
// tape, only silence left), and crossing block 1 pushes a loop frame. Stopping
// last leaves playing false against the captured true.
func driveTapeEverything(t *testing.T, tp *TapePlayer) {
	t.Helper()
	const step = 50000 // ~one frame of guest time
	for i := 0; ; i++ {
		if i > 2000 {
			t.Fatalf("drive sequence never reached the pause inside the loop: block=%d consumed=%v",
				tp.blockIdx, tp.dataConsumed)
		}
		tp.Update(step)
		if tp.blockIdx == 2 && tp.dataConsumed {
			break
		}
	}
	tp.Stop()
}

func decodeTapeState(t *testing.T, b []byte) tapeState {
	t.Helper()
	var s tapeState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding tape state: %v", err)
	}
	return s
}

func TestEveryCapturedTapeFieldSurvivesARoundTrip(t *testing.T) {
	tp := primedTape(t)
	want := tp.SaveState()

	driveTapeEverything(t, tp)
	if bytes.Equal(tp.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := tp.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := tp.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the round trip depends on. If a later change retunes the drive
// sequence so a field stops differing, the round trip silently stops covering
// it and a deleted restore of that field would go unnoticed.
func TestTheTapeDriveSequenceChangesEveryCapturedField(t *testing.T) {
	tp := primedTape(t)
	before := decodeTapeState(t, tp.SaveState())
	driveTapeEverything(t, tp)
	after := decodeTapeState(t, tp.SaveState())

	for _, f := range []struct {
		name string
		same bool
	}{
		{"BlockIdx", before.BlockIdx == after.BlockIdx},
		{"PulseIdx", before.PulseIdx == after.PulseIdx},
		{"PulseBlock", before.PulseBlock == after.PulseBlock},
		{"Tstate", before.Tstate == after.Tstate},
		{"LastToggle", before.LastToggle == after.LastToggle},
		// The EAR level is the parity trap: it toggles once per pulse, so an
		// even number of pulses between the capture and here would return it to
		// where it started and hide a lost restore completely.
		{"EarBit", before.EarBit == after.EarBit},
		{"Playing", before.Playing == after.Playing},
		{"DataConsumed", before.DataConsumed == after.DataConsumed},
		{"LoopStack", len(before.LoopStack) == len(after.LoopStack)},
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

// An open TZX loop frame has two numbers in it, and the round trip above only
// proves the stack changed DEPTH. This pins the frame's contents, which is what
// decides where playback jumps back to and how many times more.
func TestTheCapturedLoopFrameCarriesItsCounters(t *testing.T) {
	tp := primedTape(t)
	driveTapeEverything(t, tp)

	s := decodeTapeState(t, tp.SaveState())
	if len(s.LoopStack) != 1 {
		t.Fatalf("loop stack = %v, want one open frame", s.LoopStack)
	}
	if s.LoopStack[0].BodyStart != 2 {
		t.Errorf("loop body starts at %d, want 2", s.LoopStack[0].BodyStart)
	}
	if s.LoopStack[0].Remaining != 2 {
		t.Errorf("loop has %d iterations left, want 2 (of 3, with the first running)",
			s.LoopStack[0].Remaining)
	}
}

// The blob cannot see the pulse train, because the pulse train is not in it:
// LoadState rebuilds it from the block the cursor is on. So the one thing a
// capture comparison cannot check is checked here, by replaying the edges.
//
// Restoring a mid-pilot position against the pulses of a LATER block would
// resume in the middle of the wrong waveform, and every edge from then on would
// be wrong while every captured field looked perfect.
func TestReplayingFromACapturedPositionReproducesTheSameEdges(t *testing.T) {
	tp := primedTape(t)
	st := tp.SaveState()

	first := earLevels(tp, 400)

	// Move the cursor to a different block, so a LoadState that failed to
	// rebuild the pulse train would be left holding the wrong one.
	driveTapeEverything(t, tp)

	if err := tp.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := earLevels(tp, 400)

	if !bytes.Equal(first, second) {
		t.Error("replaying from the captured position produced a different edge sequence: " +
			"the position or the pulse train behind it is not being restored")
	}
}

// earLevels samples the EAR line over n small advances, which is what a loader's
// edge-timing loop sees.
func earLevels(tp *TapePlayer, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		if tp.Update(1100) { // shorter than a pilot pulse, so edges land in different samples
			out[i] = 1
		}
	}
	return out
}

// A capture must not alias the live player. The registry copies the blob too,
// but a device handing back a view of its own memory is worth catching here.
func TestTapeCaptureIsIndependentOfLaterChanges(t *testing.T) {
	tp := primedTape(t)
	st := tp.SaveState()
	before := decodeTapeState(t, st)

	driveTapeEverything(t, tp)
	if err := tp.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := decodeTapeState(t, tp.SaveState()); got.BlockIdx != before.BlockIdx {
		t.Errorf("block index = %d after restore, want %d", got.BlockIdx, before.BlockIdx)
	}
}

func TestTapeStateIDIsStable(t *testing.T) {
	if got := NewTapePlayer().StateID(); got != "tape" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "tape")
	}
}

func TestTapeLoadStateRejectsRubbish(t *testing.T) {
	tp := NewTapePlayer()
	if err := tp.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// The tape IMAGE is deliberately not in the capture, and that is a decision
// rather than an oversight, so it is pinned: a rewind restores the position on
// the tape in the deck, it does not put a different tape in the deck. A capture
// that carried the blocks would also cost megabytes per rewind point.
func TestTheCaptureDoesNotCarryTheTapeImage(t *testing.T) {
	tp := primedTape(t)
	blob := tp.SaveState()

	// The first block's bytes are distinctive; if any of them appear in the
	// blob, the image is being copied into every capture.
	if bytes.Contains(blob, []byte{0xFF, 0x01, 0x02, 0x03, 0x04, 0x05}) {
		t.Error("the capture contains the tape's own bytes: the image is media, not machine state")
	}
	if n := len(blob); n > 512 {
		t.Errorf("capture is %d bytes; a position is small, and anything this size means "+
			"the block list or the pulse train has crept in", n)
	}
}
