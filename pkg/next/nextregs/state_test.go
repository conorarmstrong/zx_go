package nextregs

import (
	"bytes"
	"encoding/gob"
	"testing"
)

// The NextReg file is the largest single piece of Next state, and the piece a
// rewind is most likely to get subtly wrong, because restoring it the obvious
// way — push the bytes back in through the write path — is wrong. Half the
// register file has a write side effect: NR$02 triggers a machine reset, NR$07
// changes the CPU clock, NR$50-$57 repage memory, NR$75-$79 bump the sprite
// index. Replaying those on every rewind would reset the machine it was trying
// to resume.
//
// So these tests are a replay property over a device captured MID-OPERATION —
// part way through an auto-incrementing run and between the two halves of a
// paired write — not a field-by-field comparison. A field-by-field test only
// checks the fields someone remembered to add, and a capture taken at rest
// proves nothing about resuming.

// wireFixture installs handlers with the two shapes that make this device's
// state more than "the bytes the guest last wrote": an auto-incrementing index
// (the real NR$40/$41 palette pair) and a two-write pairing whose phase and
// latched first byte are live between the halves (the real NR$44).
//
// Every byte these handlers touch lives in the dispatcher's own backing store,
// which is deliberate: it makes the replay a function of exactly what this
// device captures. The production handlers keep their state in the subsystem
// (palette.Bank's index, sprite.Engine's selected sprite), which is that
// subsystem's capture to make, not this one's.
//
// effects counts OnWrite invocations, so a restore that re-fired side effects
// is visible rather than merely suspected.
func wireFixture(d *Dispatcher, effects *int) {
	const (
		ramBase = 0xD0 // fixture "palette RAM", 16 bytes
		phase   = 0xE0 // 0 = expecting the first byte of a pair, 1 = the second
		latch   = 0xE1 // the first byte of a pair, held until the second arrives
	)

	// NR$41-shaped: store the byte, commit it at the current index, advance
	// the index. A run of these leaves the file part way through a sequence.
	d.SetOnWrite(0x41, func(disp *Dispatcher, v byte) {
		*effects++
		idx := disp.Raw(0x40)
		disp.Store(0x41, v)
		disp.Store(ramBase+(idx&0x0F), v)
		disp.Store(0x40, idx+1)
	})

	// NR$44-shaped: two writes make one commit. Captured between them, the
	// phase byte and the latched half are the whole of the operation.
	d.SetOnWrite(0x44, func(disp *Dispatcher, v byte) {
		*effects++
		if disp.Raw(phase) == 0 {
			disp.Store(latch, v)
			disp.Store(phase, 1)
			return
		}
		idx := disp.Raw(0x40)
		disp.Store(ramBase+(idx&0x0F), disp.Raw(latch)^v)
		disp.Store(0x40, idx+1)
		disp.Store(phase, 0)
	})

	// A read that is computed rather than stored, so the replay also covers
	// the path where the backing byte is not what the guest observes.
	d.SetOnRead(0x44, func(disp *Dispatcher) byte {
		return disp.Raw(latch) ^ disp.Raw(0x40)
	})
}

// primed returns a dispatcher stopped mid-operation: five writes into an
// auto-incrementing run, then the first half of a paired write, with the
// selected-register latch still pointing at the register the pair is being
// written through.
func primed(effects *int) *Dispatcher {
	d := New()
	wireFixture(d, effects)
	for i := 0; i < 5; i++ {
		d.WriteReg(0x41, byte(0xA0+i))
	}
	d.Select(0x44)
	d.WriteData(0x5A) // first half of the pair; the second half never came
	return d
}

// driveForward runs a fixed sequence of guest-visible accesses and returns
// everything observable from them: the per-access trace, every register read
// back afterwards, and the selected latch. Two runs from the same state must
// agree byte for byte.
//
// It deliberately opens with a WriteData, which goes through the selected
// latch, and finishes by selecting an unrelated register — so a capture that
// lost the latch cannot be rescued by the sequence happening to re-select the
// same one.
func driveForward(d *Dispatcher) []byte {
	var out []byte
	d.SetTracer(func(reg, val byte, write bool) {
		w := byte(0)
		if write {
			w = 1
		}
		out = append(out, reg, val, w)
	})
	defer d.SetTracer(nil)

	d.WriteData(0x3C) // completes the pair the capture was taken inside
	for i := 0; i < 6; i++ {
		d.WriteReg(0x41, byte(0x10+i))
	}
	d.Select(0x44)
	d.WriteData(0x7E) // and leaves a new pair half-written
	for r := 0; r < 256; r++ {
		out = append(out, d.ReadReg(byte(r)))
	}
	out = append(out, d.Selected())
	d.Select(0xA5)
	return out
}

// The property that matters: from a captured state, the register file must
// produce the same sequence it produced the first time.
func TestReplayingFromCapturedStateReproducesTheSameSequence(t *testing.T) {
	var effects int
	d := primed(&effects)

	st := d.SaveState()
	first := driveForward(d)

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveForward(d)

	if !bytes.Equal(first, second) {
		t.Error("the register file behaved differently on replay from the same captured state: " +
			"some part of it is not being captured")
	}
}

// The negative that gives the test above its teeth. Pushing the register
// values back in through the write path is how a snapshot format restores a
// register file, and it is what LoadState must not do: the handlers fire
// again, so the auto-increment index and the open pair's phase land somewhere
// else entirely.
func TestReplayingTheValuesThroughTheWritePathDoesNotResume(t *testing.T) {
	var effects int
	d := primed(&effects)

	var raws [256]byte
	for r := 0; r < 256; r++ {
		raws[r] = d.Raw(byte(r))
	}
	sel := d.Selected()

	st := d.SaveState()
	want := driveForward(d)

	var otherEffects int
	e := New()
	wireFixture(e, &otherEffects)
	for r := 0; r < 256; r++ {
		e.WriteReg(byte(r), raws[r])
	}
	e.Select(sel)

	if bytes.Equal(want, driveForward(e)) {
		t.Skip("this fixture's behaviour happens not to depend on the write path; " +
			"the replay test above is the binding one")
	}

	// ...and the real capture does what replaying the values could not.
	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, driveForward(d)) {
		t.Error("full state capture failed to reproduce the sequence it was taken from")
	}
}

// A restore must write the stored bytes straight into the backing array. Two
// things fall out of that and both are checked here: no OnWrite handler fires,
// and a register the write path refuses (the read-only core version, seeded
// internally with Store) still comes back.
func TestLoadStateRestoresStoredValuesWithoutRefiringSideEffects(t *testing.T) {
	var effects int
	d := primed(&effects)
	d.Store(0x01, 0x55) // read-only NR$01, as internal seeding writes it

	st := d.SaveState()

	d.Store(0x01, 0x99)
	for i := 0; i < 3; i++ {
		d.WriteReg(0x41, byte(i))
	}
	before := effects

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if effects != before {
		t.Errorf("LoadState fired %d OnWrite side effect(s): a restore must store the captured "+
			"bytes directly, or every rewind re-triggers resets, repaging and CPU speed changes",
			effects-before)
	}
	if got := d.Raw(0x01); got != 0x55 {
		t.Errorf("read-only NR$01 = %#02x after restore, want %#02x: the restore went through "+
			"the write path, which drops writes to it", got, 0x55)
	}
}

// A capture must not alias the live register file. The registry copies the
// blob too, but a device handing back a view of its own array is a bug worth
// catching where it would be introduced.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	var effects int
	d := primed(&effects)

	st := d.SaveState()
	frozen := append([]byte(nil), st...)

	for i := 0; i < 4; i++ {
		d.WriteReg(0x41, byte(0x11*i))
	}
	d.Select(0x00)

	if !bytes.Equal(st, frozen) {
		t.Error("the blob SaveState returned changed as the device ran on: it aliases live state")
	}
	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.Selected(); got != 0x44 {
		t.Errorf("selected = %#02x after restore, want %#02x", got, 0x44)
	}
	if got := d.Raw(0x40); got != 0x05 {
		t.Errorf("auto-increment index = %#02x after restore, want %#02x", got, 0x05)
	}
}

// The replay tests above assert on behaviour, and behaviour only observes the
// fields that reach it. The two tests below assert on the CAPTURE instead:
// drive the device so every captured field differs, restore, re-capture, and
// compare the blobs. That observes a field whether or not any access path
// exposes it, which is the only way a score means what it says.

// driveEverything changes every field a capture carries. Parity and
// convergence are the traps: a field that moves and comes back is as invisible
// as one that never moved, so every count here is deliberately odd and every
// value deliberately unlike the one primed() left behind.
func driveEverything(d *Dispatcher) {
	// Seven writes through the auto-incrementing register: an odd run, which
	// leaves the index NR$40 at 0x0C rather than back at primed()'s 0x05, and
	// rewrites seven bytes of the fixture's palette RAM that primed() left at
	// zero.
	for i := 0; i < 7; i++ {
		d.WriteReg(0x41, byte(0x11+i))
	}
	// A register with no handler at all, so the plain-storage path is in the
	// capture too and not only the handled one.
	d.WriteReg(0x35, 0x77)
	// Three writes through the pair, not one. One write completes the pair the
	// capture was taken inside and flips the phase byte, but leaves 0x5A
	// latched exactly where primed() put it; the second and third leave a
	// different half latched and put the phase back on the opposite side.
	d.Select(0x44)
	d.WriteData(0x3C)
	d.WriteData(0x6B)
	d.WriteData(0x9D)
	// ...and the selected latch ends on a register primed() never pointed it
	// at, so a lost restore of it cannot be rescued by the captured and the
	// driven state happening to coincide.
	d.Select(0xA5)
}

func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	var effects int
	d := primed(&effects)
	want := d.SaveState()

	driveEverything(d)
	if bytes.Equal(d.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := d.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. If a later change retunes the drive
// sequence so a field stops differing between capture and restore, the round
// trip silently stops covering it and still passes.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	var effects int
	d := primed(&effects)
	before := decodeStateForTest(t, d.SaveState())
	driveEverything(d)
	after := decodeStateForTest(t, d.SaveState())

	if before.Regs == after.Regs {
		t.Error("Regs is unchanged by the drive sequence, so a lost restore of it " +
			"would go undetected")
	}
	if before.Selected == after.Selected {
		t.Error("Selected is unchanged by the drive sequence, so a lost restore of it " +
			"would go undetected")
	}

	// Regs is a single captured field standing for 256 independently written
	// bytes, so "the array differs" is satisfied by one byte moving and would
	// still read as full coverage. These are the bytes the sequence exists to
	// move: the auto-increment index, the register the run commits through, a
	// register with no handler at all, the palette RAM the run fills, and the
	// two bytes that are the open pair's whole live state.
	for _, f := range []struct {
		what string
		reg  byte
	}{
		{"the auto-increment index NR$40", 0x40},
		{"the committed value NR$41", 0x41},
		{"unhandled plain storage NR$35", 0x35},
		{"the fixture's palette RAM", 0xD5},
		{"the open pair's phase", 0xE0},
		{"the open pair's latched first half", 0xE1},
	} {
		if before.Regs[f.reg] == after.Regs[f.reg] {
			t.Errorf("%s (NR$%02X) is unchanged by the drive sequence: the round trip covers "+
				"the register file only where the sequence moves it", f.what, f.reg)
		}
	}
}

func decodeStateForTest(t *testing.T, b []byte) nextRegsState {
	t.Helper()
	var s nextRegsState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "next.nextregs" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift",
			got, "next.nextregs")
	}
}

// Malformed input must be reported, and must leave the register file exactly
// as it was. A half-applied register file is the worst outcome available: the
// machine keeps running with some registers rewound and some not.
func TestLoadStateRejectsMalformedInputWithoutHalfApplying(t *testing.T) {
	var effects int
	d := primed(&effects)
	d.Select(0x2A)
	d.WriteReg(0x35, 0x77)

	var before [256]byte
	for r := 0; r < 256; r++ {
		before[r] = d.Raw(byte(r))
	}
	sel := d.Selected()

	good := d.SaveState()
	cases := map[string][]byte{
		"nil":       nil,
		"empty":     {},
		"rubbish":   {0xDE, 0xAD, 0xBE, 0xEF},
		"truncated": good[:len(good)/2], // decodes part way, then runs out
	}
	for name, blob := range cases {
		if err := d.LoadState(blob); err == nil {
			t.Errorf("%s state accepted: a malformed blob must be reported, not applied", name)
		}
		for r := 0; r < 256; r++ {
			if got := d.Raw(byte(r)); got != before[r] {
				t.Fatalf("%s state half-applied: NR$%02X = %#02x, want %#02x", name, r, got, before[r])
			}
		}
		if got := d.Selected(); got != sel {
			t.Fatalf("%s state half-applied: selected = %#02x, want %#02x", name, got, sel)
		}
	}
}
