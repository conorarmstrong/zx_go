package ay

import "testing"

func TestEngineDefaults(t *testing.T) {
	e := NewEngine()
	if e.Selected() != 0 {
		t.Errorf("default Selected = %d, want 0", e.Selected())
	}
	if e.Disabled() {
		t.Errorf("default Disabled = true, want false")
	}
	for i := 0; i < 3; i++ {
		if e.Chip(i) == nil {
			t.Errorf("Chip(%d) is nil", i)
		}
	}
}

func TestEngineSelectChip(t *testing.T) {
	cases := []struct {
		write byte
		want  byte
	}{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 2}, // clamp
	}
	for _, c := range cases {
		e := NewEngine()
		e.Select(c.write)
		if e.Selected() != c.want {
			t.Errorf("Select(%d): Selected = %d, want %d", c.write, e.Selected(), c.want)
		}
	}
}

func TestEngineDisableBit(t *testing.T) {
	e := NewEngine()
	e.Select(0x04) // bit 2 set
	if !e.Disabled() {
		t.Errorf("Select(0x04): Disabled should be true")
	}
	if e.Selected() != 0 {
		t.Errorf("Select(0x04): Selected = %d, want 0", e.Selected())
	}
}

func TestEngineActiveTracksSelection(t *testing.T) {
	e := NewEngine()
	// Each chip is a distinct instance — Active() must return
	// the right one as selection changes.
	a0 := e.Active()
	e.Select(1)
	a1 := e.Active()
	e.Select(2)
	a2 := e.Active()
	if a0 == a1 || a1 == a2 || a0 == a2 {
		t.Errorf("Active() returned the same chip across selections")
	}
}

func TestEngineChipOutOfRange(t *testing.T) {
	e := NewEngine()
	if e.Chip(-1) != nil || e.Chip(3) != nil || e.Chip(100) != nil {
		t.Errorf("Chip(out-of-range) should return nil")
	}
}

func TestEngineSelectRoundTrip(t *testing.T) {
	// Walk through a NextZXOS-style sequence of NextReg 0x06
	// writes and confirm each step lands in a sane state.
	e := NewEngine()

	// Initially silent on chip 0, not disabled.
	if e.Selected() != 0 || e.Disabled() {
		t.Fatalf("initial state: selected=%d disabled=%v", e.Selected(), e.Disabled())
	}

	// Select chip 2 with disable bit set (val 0x06).
	e.Select(0x06)
	if e.Selected() != 2 || !e.Disabled() {
		t.Errorf("after Select(0x06): selected=%d disabled=%v, want 2/true",
			e.Selected(), e.Disabled())
	}

	// Re-enable + select chip 0 (val 0x00). State must fully
	// reset on both axes.
	e.Select(0x00)
	if e.Selected() != 0 || e.Disabled() {
		t.Errorf("after Select(0x00): selected=%d disabled=%v, want 0/false",
			e.Selected(), e.Disabled())
	}

	// Out-of-range chip-select with disable bit set — verify the
	// clamp doesn't accidentally clear the disable flag.
	e.Select(0x07)
	if e.Selected() != 2 || !e.Disabled() {
		t.Errorf("after Select(0x07) clamp: selected=%d disabled=%v, want 2/true",
			e.Selected(), e.Disabled())
	}
}

func TestEngineResetClears(t *testing.T) {
	e := NewEngine()
	e.Select(0x06) // chip 2, disabled
	e.Reset()
	if e.Selected() != 0 {
		t.Errorf("after Reset: Selected = %d, want 0", e.Selected())
	}
	if e.Disabled() {
		t.Errorf("after Reset: Disabled = true, want false")
	}
}

// ============================================================
// Additional engine tests (iter 220).
// ============================================================

// TestEngineResetClearsAllChips — Engine.Reset() must call Reset()
// on every chip so register state is wiped, not just the engine's
// selection. After writing distinctive values to all three chips
// and resetting, every chip's registers should be at default.
func TestEngineResetClearsAllChips(t *testing.T) {
	e := NewEngine()
	for i := 0; i < 3; i++ {
		e.Select(byte(i))
		c := e.Active()
		c.WriteRegister(RegToneALow, byte(0xAA+i))
	}
	e.Reset()
	for i := 0; i < 3; i++ {
		e.Select(byte(i))
		if got := e.Active().ReadRegister(RegToneALow); got != 0 {
			t.Errorf("chip %d after Reset: ToneALow = $%02X, want 0", i, got)
		}
	}
}

// TestEngineSetChip_OutOfRange_NoOp.
func TestEngineSetChip_OutOfRange_NoOp(t *testing.T) {
	e := NewEngine()
	orig := e.Chip(0)
	replacement := New()
	e.SetChip(-1, replacement)
	e.SetChip(3, replacement)
	e.SetChip(99, replacement)
	if e.Chip(0) != orig {
		t.Error("out-of-range SetChip clobbered chip 0")
	}
}

// TestEngineSetChip_Nil_Ignored.
func TestEngineSetChip_Nil_Ignored(t *testing.T) {
	e := NewEngine()
	orig := e.Chip(1)
	e.SetChip(1, nil)
	if e.Chip(1) != orig {
		t.Error("SetChip(_, nil) clobbered chip 1")
	}
}

// TestEngineSetChip_Replaces verifies a valid SetChip replaces the
// chip and Active() reflects it after the next Select.
func TestEngineSetChip_Replaces(t *testing.T) {
	e := NewEngine()
	replacement := New()
	replacement.WriteRegister(RegToneALow, 0x42)
	e.SetChip(0, replacement)
	e.Select(0)
	if e.Active() != replacement {
		t.Error("after SetChip(0, replacement): Active() != replacement")
	}
	if e.Active().ReadRegister(RegToneALow) != 0x42 {
		t.Error("replacement chip's prior state was lost")
	}
}

// TestEngineSelectMaskedToLowBits verifies bits 7:3 are ignored on
// Select (only bits 2:0 used: bit 2 = disable, bits 1:0 = chip).
func TestEngineSelectMaskedToLowBits(t *testing.T) {
	e := NewEngine()
	e.Select(0xF8) // bits 7:3 set, bits 2:0 = 000
	if e.Selected() != 0 || e.Disabled() {
		t.Errorf("Select($F8): Selected=%d Disabled=%v, want 0/false (bits 7:3 ignored)",
			e.Selected(), e.Disabled())
	}
}
