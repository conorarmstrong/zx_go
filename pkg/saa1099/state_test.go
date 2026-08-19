package saa1099

import (
	"bytes"
	"reflect"
	"testing"
)

// Capture coverage for the SAA1099.
//
// The 32 registers round-trip trivially and are not what makes this worth
// testing. Six tone phases, two noise phases, two LFSRs and two envelope
// positions decide the next sample, none of them is readable through any port,
// and none of them is in any snapshot format. Restoring the registers alone
// puts the chip back at the right pitch and the wrong point in its cycle.
//
// The fixtures are run against EACH OTHER rather than against one
// perturbation. A single capture-perturb-restore test only covers the fields
// the perturbation happens to move, so a dropped restore of anything else
// passes; pairing every fixture with every other means each field is covered by
// whichever pair disagrees about it, and the coverage guard proves that set is
// never empty.

type fixture struct {
	name  string
	build func() *SAA
}

func fixtures() []fixture {
	return []fixture{
		{"power on", New},
		{"a tone", func() *SAA {
			s := New()
			s.WriteRegister(0x00, 0x9F) // channel 0 amplitude, both sides
			s.WriteRegister(0x08, 0x40) // channel 0 frequency
			s.WriteRegister(0x10, 0x35) // octaves for channels 0 and 1
			s.WriteRegister(0x14, 0x01) // tone enable
			s.WriteRegister(0x1C, 0x01) // sound enable
			return s
		}},
		{"a tone part played", func() *SAA {
			s := New()
			s.WriteRegister(0x00, 0x9F)
			s.WriteRegister(0x08, 0x40)
			s.WriteRegister(0x10, 0x35)
			s.WriteRegister(0x14, 0x01)
			s.WriteRegister(0x1C, 0x01)
			s.GenerateStereo(make([]int16, 512)) // advances the tone phases
			return s
		}},
		{"noise", func() *SAA {
			s := New()
			s.WriteRegister(0x01, 0x77) // channel 1 amplitude
			s.WriteRegister(0x15, 0x02) // noise on channel 1
			s.WriteRegister(0x16, 0x33) // both noise generators at a fast rate
			s.WriteRegister(0x1C, 0x01)
			s.GenerateStereo(make([]int16, 2048)) // advances the LFSRs
			return s
		}},
		{"a repeating envelope part way through", func() *SAA {
			s := New()
			s.WriteRegister(0x00, 0x9F)
			s.WriteRegister(0x08, 0x40)
			s.WriteRegister(0x09, 0x80) // channel 1: clocks envelope generator 0
			s.WriteRegister(0x10, 0x75)
			s.WriteRegister(0x14, 0x01)
			// Bit 7 enables; bits 3:1 are the shape. 7 is repetitive attack,
			// which runs for ever and so never latches Done.
			s.WriteRegister(0x18, 0x80|7<<1)
			s.WriteRegister(0x1C, 0x01)
			s.GenerateStereo(make([]int16, 2048))
			return s
		}},
		{"a single-shot envelope run out", func() *SAA {
			// Done latches only on the single-shot shapes (2, 4 and 6). A
			// repeating shape never sets it, which is why the first version of
			// this fixture — shape 7 — left Done unmoved and unverified.
			s := New()
			s.WriteRegister(0x00, 0x9F)
			s.WriteRegister(0x08, 0x40)
			s.WriteRegister(0x09, 0xFF) // a fast clock for the envelope
			s.WriteRegister(0x10, 0x75) // channel 1 at octave 7
			s.WriteRegister(0x14, 0x01)
			s.WriteRegister(0x18, 0x80|2<<1) // enabled, single decay
			s.WriteRegister(0x19, 0x80|4<<1) // generator 1: single triangle
			s.WriteRegister(0x1C, 0x01)
			s.GenerateStereo(make([]int16, 44100)) // a second: long past 16 steps
			return s
		}},
		{"a register selected", func() *SAA {
			s := New()
			s.WriteAddress(0x0B)
			return s
		}},
	}
}

func decodeState(t *testing.T, blob []byte) saaState {
	t.Helper()
	var st saaState
	if err := decodeForTest(blob, &st); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return st
}

// Every captured field must be moved by some fixture, or the round-trip tests
// say nothing about it. Walking the wire struct by reflection rather than by a
// hand-written list means a field added to the capture and forgotten here shows
// up as a field nothing moves.
func TestEveryCapturedFieldIsMovedBySomeFixture(t *testing.T) {
	fx := fixtures()
	base := reflect.ValueOf(decodeState(t, fx[0].build().SaveState()))

	moved := map[string]string{}
	for _, f := range fx[1:] {
		got := reflect.ValueOf(decodeState(t, f.build().SaveState()))
		for i := 0; i < base.NumField(); i++ {
			name := base.Type().Field(i).Name
			if _, done := moved[name]; done {
				continue
			}
			if !reflect.DeepEqual(base.Field(i).Interface(), got.Field(i).Interface()) {
				moved[name] = f.name
			}
		}
	}
	for i := 0; i < base.NumField(); i++ {
		name := base.Type().Field(i).Name
		if _, ok := moved[name]; !ok {
			t.Errorf("no fixture moves %s off its power-on value, so every round-trip test "+
				"passes for it whether or not it is restored", name)
		}
	}
}

// Restoring any fixture's state into any other produces a chip that re-captures
// as the one it was given.
func TestEveryPairOfFixturesRoundTrips(t *testing.T) {
	fx := fixtures()
	for _, from := range fx {
		for _, to := range fx {
			if from.name == to.name {
				continue
			}
			t.Run(from.name+" restored to "+to.name, func(t *testing.T) {
				live := from.build()
				want := to.build().SaveState()
				if bytes.Equal(live.SaveState(), want) {
					t.Skip("these two fixtures capture identically, so the pair proves nothing")
				}
				if err := live.LoadState(want); err != nil {
					t.Fatalf("LoadState: %v", err)
				}
				if got := live.SaveState(); !bytes.Equal(want, got) {
					t.Error("the restored chip does not re-capture as the chip that was captured")
				}
			})
		}
	}
}

// The property that matters: a restored chip produces the samples the original
// would have produced next. Comparing waveforms rather than fields is what makes
// this independent of which fields anyone remembered to add.
func TestReplayingFromACaptureReproducesTheSameSamples(t *testing.T) {
	live := fixtures()[2].build() // a tone part played
	blob := live.SaveState()

	want := make([]int16, 1024)
	live.GenerateStereo(want)

	restored := New()
	if err := restored.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	got := make([]int16, 1024)
	restored.GenerateStereo(got)

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d: the chip resumed from a different point "+
				"in its cycle", i, got[i], want[i])
		}
	}
}

// A malformed blob is refused whole rather than applied in part.
func TestLoadStateRejectsRubbish(t *testing.T) {
	s := fixtures()[1].build()
	want := s.SaveState()

	for _, blob := range [][]byte{nil, {}, []byte("not gob")} {
		if err := s.LoadState(blob); err == nil {
			t.Errorf("LoadState(%q) returned nil, want an error", blob)
		}
	}
	if got := s.SaveState(); !bytes.Equal(want, got) {
		t.Error("a rejected LoadState still changed the chip")
	}
}
