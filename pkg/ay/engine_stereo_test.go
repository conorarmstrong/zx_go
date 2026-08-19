package ay

import "testing"

// The TurboSound engine owns the panning, because both registers that drive it
// are engine-wide rather than per chip:
//
//	nr_08_psg_stereo_mode <= nr_wr_dat(5);          zxnext.vhd:5177
//	nr_09_psg_mono        <= nr_wr_dat(7 downto 5); zxnext.vhd:5186
//
// stereo_mode_i is a single bit shared by all three PSGs; mono_mode_i is three
// bits, one each. The VHDL slice assignment maps the high bit of the field to
// the high index, so NR$09 bit 5 belongs to PSG 0, bit 6 to PSG 1 and bit 7 to
// PSG 2 — the opposite order to the way the byte reads.

// loudChip programmes a chip so channel A alone produces a steady level, which
// is hard left in either stereo mode and centred in mono.
func loudChip(t *testing.T, a *AY) {
	t.Helper()
	steadyLevels(t, a, 15, 0, 0)
}

// stereoOf reports whether a chip's contribution is panned, by sounding it
// alone through the engine.
func pannedThroughEngine(e *Engine) bool {
	buf := make([]int16, 2)
	e.MixIntoStereo(buf)
	return buf[0] != buf[1]
}

// NR$08 bit 5 selects the law for every chip at once.
func TestTheStereoModeAppliesToEveryChip(t *testing.T) {
	e := NewEngine()
	e.SetStereoMode(true) // NR$08 bit 5 set → ACB
	for i := 0; i < 3; i++ {
		if got := e.Chip(i).StereoModeSetting(); got != StereoACB {
			t.Errorf("chip %d = %v, want ACB: NR$08 bit 5 is shared by all three PSGs", i, got)
		}
	}
	e.SetStereoMode(false) // clear → ABC
	for i := 0; i < 3; i++ {
		if got := e.Chip(i).StereoModeSetting(); got != StereoABC {
			t.Errorf("chip %d = %v, want ABC", i, got)
		}
	}
}

// NR$09 bits 7:5 force individual chips back to mono, and the bit order is the
// one thing here that cannot be guessed: bit 5 is PSG 0, not PSG 2.
func TestTheMonoMaskIsPerChipAndBit5IsPSG0(t *testing.T) {
	for _, tc := range []struct {
		mask byte
		chip int
	}{
		{mask: 1 << 5, chip: 0},
		{mask: 1 << 6, chip: 1},
		{mask: 1 << 7, chip: 2},
	} {
		e := NewEngine()
		e.SetStereoMode(false) // ABC everywhere to begin with
		e.SetMonoMask(tc.mask)

		for i := 0; i < 3; i++ {
			got := e.Chip(i).StereoModeSetting()
			want := StereoABC
			if i == tc.chip {
				want = StereoMono
			}
			if got != want {
				t.Errorf("mask %#02x: chip %d = %v, want %v", tc.mask, i, got, want)
			}
		}
	}
}

// A chip forced to mono must stay mono when the shared stereo bit is later
// flipped. In the VHDL the mono mux wins outright: mono_mode_i(n) appears in
// every one of the three mux conditions for PSG n, so stereo_mode_i cannot
// reach a chip that is held mono.
func TestTheMonoMaskOutranksTheStereoBit(t *testing.T) {
	e := NewEngine()
	e.SetMonoMask(1 << 5) // PSG 0 mono
	e.SetStereoMode(true) // then ask for ACB everywhere

	if got := e.Chip(0).StereoModeSetting(); got != StereoMono {
		t.Errorf("chip 0 = %v, want mono: NR$09 must outrank NR$08 bit 5", got)
	}
	if got := e.Chip(1).StereoModeSetting(); got != StereoACB {
		t.Errorf("chip 1 = %v, want ACB: only the masked chip is held mono", got)
	}
}

// The panning has to survive SetChip. The Next adopts the ULA's existing AY as
// the engine's chip 0 rather than carrying two instances, and that swap happens
// during wiring — after the boot defaults have already been applied. A chip
// installed afterwards must inherit the engine's current mode, or the machine's
// own AY would be the one chip left unpanned.
func TestAnAdoptedChipInheritsThePanning(t *testing.T) {
	e := NewEngine()
	e.SetStereoMode(true) // ACB

	adopted := New()
	e.SetChip(0, adopted)

	if got := adopted.StereoModeSetting(); got != StereoACB {
		t.Errorf("adopted chip = %v, want ACB: SetChip must re-apply the engine's mode", got)
	}
}

// End to end through the engine's mixer: a panned chip produces different
// levels on the two outputs, a mono one does not.
func TestTheEngineMixReflectsThePanning(t *testing.T) {
	e := NewEngine()
	loudChip(t, e.Chip(0))

	e.SetStereoMode(false) // ABC: channel A is hard left
	if !pannedThroughEngine(e) {
		t.Error("engine output is identical on both channels with ABC selected")
	}

	e.SetMonoMask(1 << 5) // hold PSG 0 mono
	if pannedThroughEngine(e) {
		t.Error("engine output is still panned after PSG 0 was held mono")
	}
}

// A disabled engine contributes nothing to either channel, the same rule the
// mono mixer already follows (NR$06 bits 1:0 = 11 holds all AY in reset).
func TestADisabledEngineMixesNothingIntoEitherChannel(t *testing.T) {
	e := NewEngine()
	loudChip(t, e.Chip(0))
	e.SetStereoMode(false)
	e.Select(0x03)

	buf := []int16{7, 9}
	e.MixIntoStereo(buf)
	if buf[0] != 7 || buf[1] != 9 {
		t.Errorf("buffer = %v, want it untouched at [7 9]", buf)
	}
}

// The panning is engine state and has to be captured, for the reason the LoRes
// wiring documents: nextregs.LoadState assigns its register array directly
// without firing any OnWrite handler, so a mode cached down here would survive
// a rewind while NR$08 moved underneath it.
func TestThePanningSurvivesACaptureAndRestore(t *testing.T) {
	before := NewEngine()
	before.SetStereoMode(true) // ACB
	before.SetMonoMask(1 << 6) // PSG 1 held mono
	blob := before.SaveState()

	after := NewEngine()
	if err := after.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := after.Chip(0).StereoModeSetting(); got != StereoACB {
		t.Errorf("chip 0 after restore = %v, want ACB", got)
	}
	if got := after.Chip(1).StereoModeSetting(); got != StereoMono {
		t.Errorf("chip 1 after restore = %v, want mono (the mask was not restored)", got)
	}
}
