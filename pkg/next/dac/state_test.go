package dac

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*Bank)(nil)

// The Next DAC bank looks like four latched bytes and is not four latched
// bytes.
//
// What determines the next frame of audio is the level carried over from the
// previous frame plus the writes that have been timed but not yet mixed: a port
// write only appends an event, and nothing is folded into the carried level
// until GenerateFrame runs. A capture taken part way through a frame that
// stopped at "the four current channel values" would restore a bank missing
// every write since the last frame boundary. It would run, at the right
// volume, playing a different note.
//
// The four channel latches matter for the frames that come after, not the one
// in flight: every event records the mean of all four channels, so a channel
// the guest has not written for a while still sets the level of the next event
// on any other channel.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// Frame geometry for these tests: 16 samples over 896 T-states, so each sample
// spans 56 T-states and the scripted writes land in distinguishable samples.
const (
	testSamples = 16
	testTStates = 896
)

// portWrite is one OUT in a replay script.
type portWrite struct {
	port uint16
	t    int
	val  byte
}

// Record only fires after a DAC port write (pkg/ula/ula.go:1666-1672), so
// whichever channel a script writes first has its restored latch overwritten
// before any event can mix it in: that channel's captured level is invisible
// in the audio that script produces. Rather than reach past the audio and read
// the levels back, the property is replayed under two scripts that differ in
// which channels they touch. Between them every one of the four latches
// reaches the output.
//
// The offsets ascend, as they do on a real machine where writes arrive in
// T-state order, and they start after the offsets primed() leaves pending.
var scripts = []struct {
	name   string
	writes []portWrite
}{
	// Touches A and C only. B and D are never written, so their restored
	// levels feed every event mean; C's restored level feeds the first one.
	// A's restored level is not observable under this script.
	{"A and C", []portWrite{
		{0x1F, 150, 0x20}, // channel A
		{0x4F, 300, 0xB0}, // channel C
		{0xF1, 500, 0x60}, // channel A again, through its alias port
		{0xF9, 700, 0x08}, // channel C
		{0x1F, 890, 0xD0}, // inside the final sample
	}},
	// Touches B and D only, which is what covers channel A's restored level.
	{"B and D", []portWrite{
		{0x0F, 120, 0x30}, // channel B
		{0xFB, 280, 0xA0}, // channel D
		{0xF3, 460, 0x70}, // channel B through its alias port
		{0xFB, 640, 0x14}, // channel D
		{0xFB, 880, 0xC0}, // inside the final sample
	}},
}

// drive replays a script through the bank exactly as the machine does — the
// ULA forwards the write, then records the resulting output pair at the
// T-state it landed on — and returns the audio produced, flattened for
// comparison.
func drive(b *Bank, writes []portWrite, frames int) []byte {
	var out []byte
	for f := 0; f < frames; f++ {
		for _, w := range writes {
			if !b.WritePort(w.port, w.val) {
				panic("test script wrote to a port the bank does not decode")
			}
			b.Record(w.t)
		}
		out = append(out, pcm(b.GenerateFrameStereo(testSamples, testTStates))...)
	}
	return out
}

// primed returns a bank part way through a frame: four channels sitting at
// four distinct levels, an output pair carried over from an earlier frame, and
// two writes already timed for the frame that has not been generated yet.
// Their offsets sit before the first offset either script uses, so the
// combined event stream stays in T-state order.
func primed() *Bank {
	b := New()
	b.WritePort(0x1F, 0x90) // A
	b.WritePort(0x0F, 0x24) // B
	b.WritePort(0x4F, 0xC8) // C
	b.WritePort(0xFB, 0x50) // D
	b.Record(0)
	b.Record(testTStates / 2)
	b.GenerateFrameStereo(testSamples, testTStates) // carries the output pair forward

	// ...and stop part way through the next frame, mid-sequence.
	b.WritePort(0xF3, 0x0C) // B, through its alias port
	b.Record(40)
	b.WritePort(0xF9, 0xE4) // C
	b.Record(90)
	return b
}

// perturb runs the bank on into a different state, the way a session does
// between taking a capture and rewinding to it: frames get generated (which
// consumes the pending writes and moves the carried level) and the guest keeps
// writing channels. Every captured field differs afterwards, and every channel
// differs by enough that dropping any single one of them shifts its pair's mean.
//
// That "every captured field differs" is what every mutation score in this file
// rests on, so it is asserted rather than trusted: retune a value here to match
// what primed() left behind and TestTheDriveSequenceChangesEveryCapturedField
// names the field that stopped being covered.
func perturb(b *Bank) {
	b.WritePort(0x1F, 0x03) // A
	b.WritePort(0x0F, 0x40) // B
	b.WritePort(0x4F, 0x11) // C
	b.WritePort(0xFB, 0x20) // D
	b.Record(20)
	b.GenerateFrameStereo(testSamples, testTStates)
	b.WritePort(0x1F, 0x77) // A, leaving one write timed but not yet mixed
	b.Record(600)
}

// The property that matters: from a captured state, the bank must produce the
// same audio it produced the first time.
func TestReplayingFromCapturedStateReproducesTheSameAudio(t *testing.T) {
	for _, sc := range scripts {
		t.Run(sc.name, func(t *testing.T) {
			b := primed()

			st := b.SaveState()
			first := drive(b, sc.writes, 3)

			perturb(b)

			if err := b.LoadState(st); err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			second := drive(b, sc.writes, 3)

			if !bytes.Equal(first, second) {
				t.Error("the DAC bank produced different audio on replay from the same " +
					"captured state: some part of the device is not being captured")
			}
		})
	}
}

// The negative that gives the test above its teeth. A snapshot-style capture of
// just the four channel values round-trips those perfectly and still replays
// the wrong audio, because the carried level and the writes that were timed but
// not yet mixed are gone. This proves the audio actually depends on more than
// the four latches, so the replay test is measuring something.
func TestTheFourChannelLatchesAloneAreNotEnough(t *testing.T) {
	b := primed()
	st := b.SaveState()

	// What a snapshot format would store, and nothing else.
	naive := New()
	naive.WritePort(0x1F, b.Level(ChannelA))
	naive.WritePort(0x0F, b.Level(ChannelB))
	naive.WritePort(0x4F, b.Level(ChannelC))
	naive.WritePort(0xFB, b.Level(ChannelD))

	want := drive(b, scripts[0].writes, 2)
	if bytes.Equal(want, drive(naive, scripts[0].writes, 2)) {
		t.Skip("this fixture's output happens not to depend on the carried level or the " +
			"pending writes; the replay test above is the binding one")
	}

	// ...and the full capture does what the four latches alone could not.
	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, drive(b, scripts[0].writes, 2)) {
		t.Error("full state capture failed to reproduce the audio it was taken from")
	}
}

// A capture must not alias the live event buffer. GenerateFrame truncates that
// buffer in place and subsequent writes refill the same backing array, so a
// capture holding a view of it would be quietly rewritten by the machine
// carrying on running.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	b := primed()
	st := b.SaveState()

	// Truncate and refill the live buffer several times over.
	for i := 0; i < 4; i++ {
		b.WritePort(0x1F, byte(0x10*i))
		b.Record(i * 100)
		b.WritePort(0xFB, byte(0xF0-0x10*i))
		b.Record(i*100 + 40)
		b.GenerateFrameStereo(testSamples, testTStates)
	}

	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	want := drive(primed(), scripts[0].writes, 2)
	if got := drive(b, scripts[0].writes, 2); !bytes.Equal(want, got) {
		t.Error("the capture did not survive the device running on: it aliased live state")
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "next.dac" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.dac")
	}
}

// A blob that cannot be decoded must be refused whole. Half-applying one would
// leave a bank that runs, holding a mixture of two different moments.
func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"garbage", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"empty", nil},
		{"truncated", primed().SaveState()[:8]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := primed()
			if err := b.LoadState(tc.blob); err == nil {
				t.Fatal("a malformed state blob must be reported, not half-applied")
			}
			want := drive(primed(), scripts[0].writes, 2)
			if got := drive(b, scripts[0].writes, 2); !bytes.Equal(want, got) {
				t.Error("a rejected state blob changed the device anyway")
			}
		})
	}
}

// pcm flattens samples so two runs can be compared as bytes.
func pcm(s []int16) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, v := range s {
		out = append(out, byte(uint16(v)>>8), byte(uint16(v)))
	}
	return out
}

// The replay-audio property above covers every restore in this device today —
// a mutation audit deleting each one in turn found all of them caught. What it
// does not do is keep covering them.
//
// Every one of those kills is bounded by what primed() and perturb() happen to
// make differ, and nothing asserts that they differ. Changing one byte of
// perturb() so channel B lands back on the value primed() left it at makes the
// whole suite pass, and channel B's restore can then be deleted with the suite
// still passing: the coverage is gone and nothing says so. The two tests below
// close that hole. The first asserts on the CAPTURE rather than on the audio,
// so it observes fields whether or not they reach a sample; the second asserts
// the drive sequence still moves every one of them, which is what makes the
// first one's score mean anything.

// TestEveryCapturedFieldSurvivesARoundTrip captures, drives the bank on with
// perturb (the same thing a session does between taking a capture and rewinding
// to it), restores, and re-captures. A field that is captured but not restored
// makes the second blob differ from the first.
func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	b := primed()
	want := b.SaveState()

	perturb(b)
	if bytes.Equal(b.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := b.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := b.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. Levels is deliberately checked one
// channel at a time: it is a single gob field but four independent restores,
// and comparing the array whole would report it as covered when only one
// channel had moved — which is how three of the four could quietly stop being
// covered at all.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	b := primed()
	before := decodeStateForTest(t, b.SaveState())
	perturb(b)
	after := decodeStateForTest(t, b.SaveState())

	for _, f := range []struct {
		name string
		same bool
	}{
		{"Levels[A]", before.Levels[ChannelA] == after.Levels[ChannelA]},
		{"Levels[B]", before.Levels[ChannelB] == after.Levels[ChannelB]},
		{"Levels[C]", before.Levels[ChannelC] == after.Levels[ChannelC]},
		{"Levels[D]", before.Levels[ChannelD] == after.Levels[ChannelD]},
		{"StartL", before.StartL == after.StartL},
		{"StartR", before.StartR == after.StartR},
		// The pending-event buffer is four restores, not one: the truncation
		// that clears what is already there, and the three parts of each event.
		// A restore that dropped only the right-hand level would leave every
		// offset and every left-hand level right.
		{"len(Events)", len(before.Events) == len(after.Events)},
		{"Events[].TStateOffset", sameOffsets(before.Events, after.Events)},
		{"Events[].Left", sameLefts(before.Events, after.Events)},
		{"Events[].Right", sameRights(before.Events, after.Events)},
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

func sameOffsets(a, b []eventState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].TStateOffset != b[i].TStateOffset {
			return false
		}
	}
	return true
}

func sameLefts(a, b []eventState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Left != b[i].Left {
			return false
		}
	}
	return true
}

func sameRights(a, b []eventState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Right != b[i].Right {
			return false
		}
	}
	return true
}

func decodeStateForTest(t *testing.T, b []byte) bankState {
	t.Helper()
	var s bankState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}
