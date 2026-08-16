package audiodac

import (
	"bytes"
	"testing"
)

// The DAC looks like a single latched byte and is not one.
//
// What determines the next frame of audio is the level carried over from the
// previous frame, the writes that have been timed but not yet mixed (they sit
// in the event buffer until GenerateFrame consumes them), and which of the two
// devices is plugged in — a write only reaches the DAC when an enabled device
// claims the port. None of that is readable by the guest and none of it appears
// in any snapshot format, so a capture that stopped at "the current sample
// value" would restore a DAC that runs but does not resume.
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

// portWrite is one OUT in the replay script.
type portWrite struct {
	port  byte
	t     int
	level byte
}

// script is a fixed sequence of OUTs replayed every frame. It writes to BOTH
// device ports, at different levels, so which device is plugged in changes the
// waveform: that is what gives the enable flags teeth in a replay test that
// only observes audio. The offsets ascend, as they do on a real machine where
// writes arrive in T-state order.
var script = []portWrite{
	{SpecDrumPort, 150, 0xF0},
	{CovoxPort, 300, 0x20},
	{SpecDrumPort, 500, 0x30},
	{CovoxPort, 700, 0xE0},
	{SpecDrumPort, 890, 0x90}, // inside the final sample
}

// drive runs the script through the DAC exactly as the machine does — a port
// write only reaches the device when an enabled device claims that port — and
// returns the audio produced, flattened for comparison.
func drive(d *DAC, frames int) []byte {
	var out []byte
	for f := 0; f < frames; f++ {
		for _, w := range script {
			if d.Handles(w.port) {
				d.Record(w.t, w.level)
			}
		}
		out = append(out, pcm(d.GenerateFrame(testSamples, testTStates))...)
	}
	return out
}

// primed returns a DAC part-way through a frame: SpecDrum plugged in, a level
// carried over from an earlier frame, and two writes already timed for the
// frame that has not been generated yet. Their offsets sit before the script's
// first write so the combined event stream stays in T-state order.
func primed() *DAC {
	d := New()
	d.SetSpecDrum(true)
	d.Record(0, 0x11)
	d.Record(testTStates/2, 0xC4)
	d.GenerateFrame(testSamples, testTStates) // carries 0xC4 into the next frame
	d.Record(50, 0x07)
	d.Record(100, 0xB2)
	return d
}

// perturb runs the DAC on into a different state, the way a session does
// between taking a capture and rewinding to it: frames get generated (which
// consumes the pending writes and moves the carried level) and the user
// changes which devices are plugged in. Every captured field differs
// afterwards, so a restore that drops any one of them shows up in the audio.
func perturb(d *DAC) {
	d.SetSpecDrum(false)
	d.SetCovox(true)
	d.Record(5, 0x7E)
	d.GenerateFrame(testSamples, testTStates)
	d.Record(60, 0x02)
}

// The property that matters: from a captured state, the DAC must produce the
// same audio it produced the first time.
func TestReplayingFromCapturedStateReproducesTheSameAudio(t *testing.T) {
	d := primed()

	st := d.SaveState()
	first := drive(d, 3)

	perturb(d)

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(d, 3)

	if !bytes.Equal(first, second) {
		t.Error("the DAC produced different audio on replay from the same captured state: " +
			"some part of the device is not being captured")
	}
}

// The negative that gives the test above its teeth. A snapshot-style capture of
// just "which devices are on, and the level the DAC is sitting at" round-trips
// those fields perfectly and still replays the wrong audio, because the writes
// that have been timed but not yet mixed are gone. This proves the audio
// actually depends on more than the latched level, so the replay test is
// measuring something.
func TestTheLatchedLevelAloneIsNotEnough(t *testing.T) {
	d := primed()
	st := d.SaveState()

	// What a snapshot format would store, and nothing else.
	naive := New()
	naive.SetSpecDrum(d.specDrum)
	naive.SetCovox(d.covox)
	naive.startLevel = d.startLevel

	want := drive(d, 2)
	if bytes.Equal(want, drive(naive, 2)) {
		t.Skip("this fixture's output happens not to depend on the pending writes; " +
			"the replay test above is the binding one")
	}

	// ...and the full capture does what the latched level alone could not.
	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, drive(d, 2)) {
		t.Error("full state capture failed to reproduce the audio it was taken from")
	}
}

// A capture must not alias the live event buffer. GenerateFrame truncates that
// buffer in place and subsequent writes refill the same backing array, so a
// capture holding a view of it would be quietly rewritten by the machine
// carrying on running.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	d := primed()
	st := d.SaveState()

	// Truncate and refill the live buffer several times over.
	for i := 0; i < 4; i++ {
		d.Record(i*100, byte(0x10*i))
		d.Record(i*100+40, byte(0xF0-0x10*i))
		d.GenerateFrame(testSamples, testTStates)
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	want := drive(primed(), 2)
	if got := drive(d, 2); !bytes.Equal(want, got) {
		t.Error("the capture did not survive the device running on: it aliased live state")
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "audiodac" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "audiodac")
	}
}

// A blob that cannot be decoded must be refused whole. Half-applying one would
// leave a DAC that runs, holding a mixture of two different moments.
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
			d := primed()
			if err := d.LoadState(tc.blob); err == nil {
				t.Fatal("a malformed state blob must be reported, not half-applied")
			}
			want := drive(primed(), 2)
			if got := drive(d, 2); !bytes.Equal(want, got) {
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
