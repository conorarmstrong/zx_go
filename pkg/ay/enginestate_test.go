package ay

import (
	"bytes"
	"testing"
)

// A machine has more than one AY. The Next's TurboSound Engine holds three,
// and a classic ULA has its own, so a StateID that is a constant makes
// registering them a panic rather than a capture.
//
// The Engine also holds state of its own that lives on no chip: which chip
// port $FFFD has selected, and whether NextReg $06 is holding them in reset.
// Rewind without those and the machine resumes writing registers to the wrong
// chip.

func TestTwoChipsCanCarryDistinctIdentifiers(t *testing.T) {
	a, b := New(), New()
	if a.StateID() != b.StateID() {
		t.Fatal("two fresh chips should share the default identifier")
	}
	a.SetStateID("ay.ula")
	if a.StateID() == b.StateID() {
		t.Error("SetStateID must distinguish instances, or a machine with more than one AY " +
			"cannot register them all")
	}
}

// The Engine is the device, not its chips: capturing it must carry all three
// generators AND the engine-level selection.
func TestEngineReplayReproducesTheSameMixedAudio(t *testing.T) {
	e := NewEngine()
	for i := 0; i < 3; i++ {
		c := e.Chip(i)
		c.WriteRegister(0, byte(0x40+i))
		c.WriteRegister(1, 0x01)
		c.WriteRegister(6, 0x0F)
		c.WriteRegister(7, 0x36) // active low: tone A + noise A enabled
		c.WriteRegister(8, 0x10)
		c.WriteRegister(13, 0x0E)
	}
	e.SelectChip(2)
	for i := 0; i < 4000; i++ {
		for j := 0; j < 3; j++ {
			e.Chip(j).StepTick()
		}
	}

	st := e.SaveState()
	first := make([]int16, 800)
	e.MixInto(first)

	if err := e.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := make([]int16, 800)
	e.MixInto(second)

	if !bytes.Equal(pcm(first), pcm(second)) {
		t.Error("the engine produced different audio on replay from the same captured state")
	}
}

// The field the chips cannot carry.
func TestEngineCaptureCarriesTheSelectedChipAndResetState(t *testing.T) {
	e := NewEngine()
	e.SelectChip(2)
	e.Select(0x03) // hold all AY in reset
	st := e.SaveState()

	e.SelectChip(0)
	e.Select(0x00)

	if err := e.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := e.Selected(); got != 2 {
		t.Errorf("selected chip = %d after restore, want 2: a rewind would resume writing "+
			"registers to the wrong chip", got)
	}
	if !e.Disabled() {
		t.Error("the NextReg $06 reset-hold state was not restored")
	}
}

func TestEngineStateIDIsDistinctFromABareChip(t *testing.T) {
	if NewEngine().StateID() == New().StateID() {
		t.Error("the engine and a bare chip must not share an identifier, or registering both panics")
	}
}

func TestEngineLoadStateRejectsRubbish(t *testing.T) {
	if err := NewEngine().LoadState([]byte{1, 2, 3}); err == nil {
		t.Error("a malformed engine state must be reported, not half-applied")
	}
}
