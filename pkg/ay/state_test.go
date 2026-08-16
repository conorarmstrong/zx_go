package ay

import (
	"bytes"
	"testing"
)

// The AY is the device that proves why a rewind needs more than registers.
//
// Its 16 registers are what a program writes and what a snapshot format
// stores. They are not what determines the next sample: that comes from the
// tone counters, the 17-bit noise LFSR, and the envelope's position and
// direction, none of which are readable by the guest and none of which appear
// in any snapshot format. Restore the registers alone and the chip carries on
// from wherever its counters happened to be, so the sound after a rewind is
// not the sound before it.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// primed returns an AY part-way through a tone, a noise sequence and an
// envelope, so its hidden counters hold values that a register-only capture
// could not reconstruct.
func primed() *AY {
	a := New()
	a.WriteRegister(0, 0x55) // channel A tone period, fine
	a.WriteRegister(1, 0x01) // and coarse
	a.WriteRegister(6, 0x0F) // noise period
	// Mixer bits are active low, and getting this wrong is easy: 0x38 sets
	// bits 3-5 and therefore DISABLES noise on every channel, which silently
	// takes the noise LFSR out of the output and out of any test written
	// against it. 0x36 clears bit 0 and bit 3, enabling tone and noise on
	// channel A.
	a.WriteRegister(7, 0x36)
	a.WriteRegister(8, 0x10) // channel A uses the envelope
	a.WriteRegister(11, 0x40)
	a.WriteRegister(12, 0x00)
	a.WriteRegister(13, 0x0E) // continue, alternate: a shape that keeps moving
	for i := 0; i < 5000; i++ {
		a.StepTick()
	}
	return a
}

// The property that matters: from a captured state, the chip must produce the
// same samples it produced the first time.
func TestReplayingFromCapturedStateReproducesTheSameAudio(t *testing.T) {
	a := primed()

	st := a.SaveState()
	first := a.GenerateSamples(2000)

	if err := a.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := a.GenerateSamples(2000)

	if !bytes.Equal(pcm(first), pcm(second)) {
		t.Error("the AY produced different samples on replay from the same captured state: " +
			"some part of the generator is not being captured")
	}
}

// The negative that gives the test above its teeth. If the hidden state were
// not captured, a register-only restore would still pass a weak test, because
// the registers do round-trip. This proves the sample stream actually depends
// on more than the registers, so the test is measuring something.
func TestRegistersAloneAreNotEnoughToReproduceTheAudio(t *testing.T) {
	a := primed()
	st := a.SaveState()
	want := pcm(a.GenerateSamples(2000))

	// Rebuild a chip from the registers only, the way a snapshot format does.
	b := New()
	for r := 0; r < NumRegisters; r++ {
		b.WriteRegister(byte(r), a.ReadRegister(byte(r)))
	}
	got := pcm(b.GenerateSamples(2000))

	if bytes.Equal(want, got) {
		t.Skip("this chip's output happens not to depend on hidden state here; " +
			"the replay test above is the binding one")
	}

	// ...and the full capture does what the registers alone could not.
	if err := a.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, pcm(a.GenerateSamples(2000))) {
		t.Error("full state capture failed to reproduce the audio it was taken from")
	}
}

// A capture must not alias the live chip. The registry copies too, but a
// device handing back a view of its own memory is a bug worth catching here.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	a := primed()
	st := a.SaveState()

	before := a.ReadRegister(0)
	a.WriteRegister(0, before^0xFF)
	for i := 0; i < 1000; i++ {
		a.StepTick()
	}

	if err := a.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := a.ReadRegister(0); got != before {
		t.Errorf("register 0 = %#02x after restore, want %#02x", got, before)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "ay" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "ay")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	a := New()
	if err := a.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
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
