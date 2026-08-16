package ay

import (
	"bytes"
	"encoding/gob"
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

// The replay-audio property has a blind spot, and a mutation audit found it:
// 11 of the AY's 19 field restores could be deleted with every test still
// passing. Two different causes, and both matter.
//
// Some fields do not reach the audio at all. The register-select latch decides
// where the NEXT write lands, so no amount of comparing samples can observe
// it. Others do reach the audio but the fixture never made them differ between
// capture and restore, so deleting the restore left them already correct.
//
// The fix is to stop asserting on output and assert on the capture itself:
// drive the chip so EVERY captured field changes, restore, and re-capture. If
// any field is not restored, the second blob differs from the first. That
// observes all 19 fields rather than the subset audio happens to expose.

// driveEverything changes every field a capture carries. Parity matters: an
// even number of toggles returns a flag to where it started and hides a lost
// restore, so the counts here are deliberately odd.
func driveEverything(a *AY) {
	a.SelectRegister(5)      // moves selected
	a.WriteRegister(0, 0x7F) // tone period
	a.WriteRegister(6, 0x1F) // noise period
	a.WriteRegister(7, 0x24) // a different mixer
	// A short envelope period matters as much as the tick count. With the
	// period at 0x80 the envelope advances so slowly that 2001 ticks left
	// EnvStep exactly where primed() had it, and the guard below caught that:
	// the round-trip test would have covered every field except the envelope
	// position, which is one of the fields this capture exists for.
	a.WriteRegister(11, 0x01)
	a.WriteRegister(12, 0x00)
	for i := 0; i < 999; i++ { // odd count: counters and half-toggles land differently
		a.StepTick()
	}
	// The shape write comes LAST, after the ticks, and the order is the point.
	// envPrimed is set by a shape write and cleared on the first envelope tick,
	// so writing the shape first leaves it false in both the captured and the
	// driven state, and a lost restore of it is invisible.
	//
	// Shape 0x01 against primed()'s 0x0E also flips every derived bit: 0x0E is
	// continue=1 attack=1 alternate=1 hold=0, 0x01 is continue=0 attack=0
	// alternate=0 hold=1. Sharing even one bit hides a lost restore of the
	// boolean it derives, which is how six envelope fields survived deletion
	// when this used 0x0C.
	a.WriteRegister(13, 0x01)
}

func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	a := primed()
	want := a.SaveState()

	driveEverything(a)
	if bytes.Equal(a.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := a.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := a.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. If a later change retunes the drive
// sequence so a field stops differing, the round-trip test silently stops
// covering it, exactly as the audio test had been doing for eleven fields.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	a := primed()
	before := decodeStateForTest(t, a.SaveState())
	driveEverything(a)
	after := decodeStateForTest(t, a.SaveState())

	for _, f := range []struct {
		name string
		same bool
	}{
		{"Regs", before.Regs == after.Regs},
		{"Selected", before.Selected == after.Selected},
		{"ToneCounter", before.ToneCounter == after.ToneCounter},
		{"ToneOutput", before.ToneOutput == after.ToneOutput},
		{"NoiseCounter", before.NoiseCounter == after.NoiseCounter},
		{"NoiseLFSR", before.NoiseLFSR == after.NoiseLFSR},
		{"NoiseHalf", before.NoiseHalf == after.NoiseHalf},
		{"EnvCounter", before.EnvCounter == after.EnvCounter},
		{"EnvStep", before.EnvStep == after.EnvStep},
		{"Shape", before.Shape == after.Shape},
		{"EnvInc", before.EnvInc == after.EnvInc},
		{"EnvContinue", before.EnvContinue == after.EnvContinue},
		{"EnvAlt", before.EnvAlt == after.EnvAlt},
		{"EnvHold", before.EnvHold == after.EnvHold},
		{"EnvAttack", before.EnvAttack == after.EnvAttack},
		{"EnvPrimed", before.EnvPrimed == after.EnvPrimed},
		// EnvHolding is deliberately absent: it and EnvPrimed cannot both
		// differ in one sequence, because a fresh shape write sets EnvPrimed
		// and clears EnvHolding, while ticking to a held envelope does the
		// reverse. TestARoundTripFromAHeldEnvelope covers it instead.
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

func decodeStateForTest(t *testing.T, b []byte) ayState {
	t.Helper()
	var s ayState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// EnvHolding is the one field driveEverything cannot move, so it gets its own
// round trip from the opposite direction: a one-shot envelope ticked to
// completion, which locks at an end value. Without this the field would be
// captured, restored, and covered by nothing.
func TestARoundTripFromAHeldEnvelope(t *testing.T) {
	a := New()
	a.WriteRegister(11, 0x01)
	a.WriteRegister(12, 0x00)
	a.WriteRegister(13, 0x00) // one-shot: runs down once, then holds
	for i := 0; i < 4000; i++ {
		a.StepTick()
	}
	held := decodeStateForTest(t, a.SaveState())
	if !held.EnvHolding {
		t.Fatalf("the fixture did not reach a held envelope (EnvStep=%d), so it covers nothing", held.EnvStep)
	}

	want := a.SaveState()
	a.WriteRegister(13, 0x0E) // a continuing shape clears the hold
	for i := 0; i < 333; i++ {
		a.StepTick()
	}
	if moved := decodeStateForTest(t, a.SaveState()); moved.EnvHolding {
		t.Fatal("the drive step did not clear EnvHolding, so a lost restore of it stays invisible")
	}

	if err := a.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := a.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after restoring a held envelope did not reproduce the capture")
	}
}
