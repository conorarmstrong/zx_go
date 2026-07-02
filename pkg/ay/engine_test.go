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

// SelectChip (the TurboSound $FFFD protocol) picks the active chip; higher
// values clamp to chip 2.
func TestEngineSelectChip(t *testing.T) {
	cases := []struct {
		write byte
		want  byte
	}{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 2}, // clamp
		{9, 2},
	}
	for _, c := range cases {
		e := NewEngine()
		e.SelectChip(c.write)
		if e.Selected() != c.want {
			t.Errorf("SelectChip(%d): Selected = %d, want %d", c.write, e.Selected(), c.want)
		}
	}
}

// NextReg 0x06 audio-chip mode (bits 1-0): only 11 ("hold all AY in reset")
// silences the engine. Crucially, bit 2 is PS/2 mode and must NOT disable AY —
// NextZXOS sets it during boot, and misreading it as "AY disable" would mute
// AY music whenever the Next boots.
func TestEngineAudioChipMode(t *testing.T) {
	for _, c := range []struct {
		val      byte
		disabled bool
		note     string
	}{
		{0x00, false, "YM mode active"},
		{0x01, false, "AY mode active"},
		{0x02, false, "ZXN-8950 active"},
		{0x03, true, "hold all AY in reset"},
		{0x04, false, "bit 2 = PS/2 mode, not AY-disable"},
		{0xA0, false, "reset default (bits 1-0 = 00)"},
		{0xF8, false, "high bits set but bits 1-0 = 00 → active"},
		{0xFF, true, "bits 1-0 = 11 → hold in reset, even with high bits set"},
	} {
		e := NewEngine()
		e.Select(c.val)
		if e.Disabled() != c.disabled {
			t.Errorf("Select($%02X): Disabled = %v, want %v (%s)", c.val, e.Disabled(), c.disabled, c.note)
		}
	}
}

func TestEngineActiveTracksSelection(t *testing.T) {
	e := NewEngine()
	// Each chip is a distinct instance — Active() must return
	// the right one as the TurboSound selection changes.
	a0 := e.Active()
	e.SelectChip(1)
	a1 := e.Active()
	e.SelectChip(2)
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
	// The chip select (SelectChip / $FFFD) and the audio mode (Select / NR$06)
	// are independent axes — a NR$06 write must never change the active chip.
	e := NewEngine()
	if e.Selected() != 0 || e.Disabled() {
		t.Fatalf("initial state: selected=%d disabled=%v", e.Selected(), e.Disabled())
	}

	e.SelectChip(2)
	e.Select(0x03) // hold all AY in reset
	if e.Selected() != 2 || !e.Disabled() {
		t.Errorf("after SelectChip(2)+Select(0x03): selected=%d disabled=%v, want 2/true",
			e.Selected(), e.Disabled())
	}

	e.Select(0x00) // YM active — must re-enable WITHOUT touching the chip
	if e.Selected() != 2 || e.Disabled() {
		t.Errorf("after Select(0x00): selected=%d disabled=%v, want 2/false",
			e.Selected(), e.Disabled())
	}
}

func TestEngineResetClears(t *testing.T) {
	e := NewEngine()
	e.SelectChip(2)
	e.Select(0x03) // chip 2, disabled
	e.Reset()
	if e.Selected() != 0 {
		t.Errorf("after Reset: Selected = %d, want 0", e.Selected())
	}
	if e.Disabled() {
		t.Errorf("after Reset: Disabled = true, want false")
	}
}

// TestEngineResetClearsAllChips — Engine.Reset() must call Reset()
// on every chip so register state is wiped, not just the engine's
// selection. After writing distinctive values to all three chips
// and resetting, every chip's registers should be at default.
func TestEngineResetClearsAllChips(t *testing.T) {
	e := NewEngine()
	for i := 0; i < 3; i++ {
		e.SelectChip(byte(i))
		c := e.Active()
		c.WriteRegister(RegToneALow, byte(0xAA+i))
	}
	e.Reset()
	for i := 0; i < 3; i++ {
		e.SelectChip(byte(i))
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

// TestEngineSelectOnlyBits10Matter verifies Select only consults NR$06 bits
// 1-0 (the audio chip mode); all other bits — including bit 2 (PS/2) — are
// irrelevant to whether AY is silenced.
func TestEngineSelectOnlyBits10Matter(t *testing.T) {
	e := NewEngine()
	e.Select(0xFC) // bits 1-0 = 00 (YM, active); every other bit set
	if e.Disabled() {
		t.Errorf("Select($FC): bits 1-0 == 00 should be active regardless of other bits")
	}
}
