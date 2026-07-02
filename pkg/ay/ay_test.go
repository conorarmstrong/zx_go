package ay

import (
	"testing"
)

func TestNewResetsRegisters(t *testing.T) {
	a := New()
	for i := 0; i < NumRegisters; i++ {
		expected := byte(0)
		if i == RegMixer {
			expected = 0x3F // all channels muted by default
		}
		if got := a.ReadRegister(byte(i)); got != expected {
			t.Errorf("register %d: expected %02X, got %02X", i, expected, got)
		}
	}
}

func TestSelectAndReadRegister(t *testing.T) {
	a := New()
	a.WriteRegister(RegToneALow, 0xAB)
	a.SelectRegister(RegToneALow)
	if got := a.ReadSelected(); got != 0xAB {
		t.Errorf("ReadSelected: expected 0xAB, got %02X", got)
	}
}

func TestWriteSelected(t *testing.T) {
	a := New()
	a.SelectRegister(RegVolumeA)
	a.WriteSelected(0x0A)
	if got := a.ReadRegister(RegVolumeA); got != 0x0A {
		t.Errorf("VolumeA: expected 0x0A, got %02X", got)
	}
}

func TestSelectedReturnsCurrentIndex(t *testing.T) {
	a := New()
	// Default selected index is 0 per soft-reset.
	if got := a.Selected(); got != 0 {
		t.Errorf("default Selected = %d, want 0", got)
	}
	for _, reg := range []byte{0, 5, RegMixer, RegVolumeA, RegEnvShape, 15} {
		a.SelectRegister(reg)
		if got := a.Selected(); got != reg {
			t.Errorf("after Select(%d): Selected = %d", reg, got)
		}
	}
	// SelectRegister masks the input to 4 bits — values >15 wrap.
	a.SelectRegister(0xAB) // = 0xAB & 0xF = 11
	if got := a.Selected(); got != 0x0B {
		t.Errorf("Select($AB): Selected = %d, want 11 ($0B)", got)
	}
}

func TestRegisterMasking(t *testing.T) {
	a := New()
	// Tone high registers should mask to 4 bits
	a.WriteRegister(RegToneAHigh, 0xFF)
	if got := a.ReadRegister(RegToneAHigh); got != 0x0F {
		t.Errorf("ToneAHigh: expected 0x0F, got %02X", got)
	}
	// Noise should mask to 5 bits
	a.WriteRegister(RegNoise, 0xFF)
	if got := a.ReadRegister(RegNoise); got != 0x1F {
		t.Errorf("Noise: expected 0x1F, got %02X", got)
	}
	// Volume registers mask to 5 bits (4 volume bits + envelope bit)
	a.WriteRegister(RegVolumeA, 0xFF)
	if got := a.ReadRegister(RegVolumeA); got != 0x1F {
		t.Errorf("VolumeA: expected 0x1F, got %02X", got)
	}
	// Envelope shape masks to 4 bits
	a.WriteRegister(RegEnvShape, 0xFF)
	if got := a.ReadRegister(RegEnvShape); got != 0x0F {
		t.Errorf("EnvShape: expected 0x0F, got %02X", got)
	}
}

func TestGenerateSamplesSilent(t *testing.T) {
	a := New()
	// Default state has all channels muted via the mixer.
	samples := a.GenerateSamples(1024)
	if len(samples) != 1024 {
		t.Fatalf("expected 1024 samples, got %d", len(samples))
	}
	for i, s := range samples {
		if s != 0 {
			t.Fatalf("sample %d: expected silence, got %d", i, s)
		}
	}
}

func TestGenerateSamplesProducesAudio(t *testing.T) {
	a := New()
	// Programme channel A: tone enabled, full volume, ~440Hz tone period.
	// Period = AYClock / (16 * 2 * freq) ~= 1773400 / (16 * 2 * 440) ~= 126
	a.WriteRegister(RegToneALow, 126)
	a.WriteRegister(RegToneAHigh, 0)
	a.WriteRegister(RegMixer, 0x3E) // enable tone A (bit 0 = 0), all noise off
	a.WriteRegister(RegVolumeA, 0x0F)

	samples := a.GenerateSamples(SampleRate / 10) // 100ms

	// Verify we get at least one positive and one zero sample (square wave).
	hasNonZero := false
	hasZero := false
	for _, s := range samples {
		if s > 0 {
			hasNonZero = true
		}
		if s == 0 {
			hasZero = true
		}
		if hasNonZero && hasZero {
			break
		}
	}
	if !hasNonZero {
		t.Errorf("expected at least one non-zero sample")
	}
	if !hasZero {
		t.Errorf("expected at least one zero sample (square wave low)")
	}
}

func TestMixIntoAddsToBuffer(t *testing.T) {
	a := New()
	a.WriteRegister(RegToneALow, 50)
	a.WriteRegister(RegToneAHigh, 0)
	a.WriteRegister(RegMixer, 0x3E) // enable tone A
	a.WriteRegister(RegVolumeA, 0x08)

	buf := make([]int16, 256)
	for i := range buf {
		buf[i] = 100
	}
	a.MixInto(buf)

	// Buffer should be modified (not all 100 anymore).
	allHundred := true
	for _, s := range buf {
		if s != 100 {
			allHundred = false
			break
		}
	}
	if allHundred {
		t.Errorf("MixInto did not modify buffer")
	}
}

func TestEnvelopeShapeStarts(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvLow, 0x10)
	a.WriteRegister(RegEnvHigh, 0)
	// Shape 0x0E: continue, attack, alternate (triangle).
	a.WriteRegister(RegEnvShape, 0x0E)
	if a.envHolding {
		t.Errorf("envelope should not be holding immediately after shape write")
	}
	if a.envStep != 0 {
		t.Errorf("envelope step should reset to 0 on attack=1, got %d", a.envStep)
	}
}

func TestResetClearsState(t *testing.T) {
	a := New()
	a.WriteRegister(RegToneALow, 0xAA)
	a.WriteRegister(RegMixer, 0x00)
	a.Reset()
	if a.ReadRegister(RegToneALow) != 0 {
		t.Errorf("Reset: ToneALow should be 0")
	}
	if a.ReadRegister(RegMixer) != 0x3F {
		t.Errorf("Reset: Mixer should default to 0x3F")
	}
}

// =========================================================================
// AY envelope shape end-state tests.
//
// Each of the 16 envelope shapes (NR$13 / RegEnvShape bits 3:0) defines
// a unique pattern. Per the AY-3-8910 / YM2149 datasheet, the
// "hold" shapes (HOLD bit set, or CONTINUE bit clear) end in a fixed
// envelope level after one cycle; the "repeating" shapes (CONTINUE=1,
// HOLD=0) keep cycling.
//
// Shape decode (bits = C A Alt H):
//
//	 0  0000  \____  (decay then hold at 0)
//	 1  0001  \____  (decay then hold at 0)
//	 2  0010  \____  (decay then hold at 0)
//	 3  0011  \____  (decay then hold at 0)
//	 4  0100  /____  (attack then hold at 0)
//	 5  0101  /____  (attack then hold at 0)
//	 6  0110  /____  (attack then hold at 0)
//	 7  0111  /____  (attack then hold at 0)
//	 8  1000  \\\\\  (repeating decay)
//	 9  1001  \____  (decay then hold at 0)
//	10  1010  \/\/   (alternating triangle)
//	11  1011  \¯¯¯¯  (decay then hold at 15)
//	12  1100  ////   (repeating attack)
//	13  1101  /¯¯¯¯  (attack then hold at 15)
//	14  1110  /\/\   (alternating triangle)
//	15  1111  /____  (attack then hold at 0)
//
// CONTINUE=0 (shapes 0-7): the FPGA-faithful behaviour is "drop to 0
// and HOLD" regardless of the other bits — our implementation matches.

// runEnvCycles advances the envelope generator by N ticks. Useful for
// driving the envelope to its steady-state end value.
func runEnvCycles(a *AY, ticks int) {
	for i := 0; i < ticks; i++ {
		a.tickEnvelope()
	}
}

// TestEnvelope_Shape0to7_HoldAtZeroAfterCycle covers all eight no-
// CONTINUE shapes. Per spec they all end at envelope level 0 with
// envHolding=true, regardless of ATTACK / ALTERNATE / HOLD bits.
func TestEnvelope_Shape0to7_HoldAtZeroAfterCycle(t *testing.T) {
	for shape := byte(0); shape <= 7; shape++ {
		a := New()
		a.WriteRegister(RegEnvShape, shape)
		// 33 ticks > one full cycle of 32 ticks, so envelope must
		// have hit its terminal state.
		runEnvCycles(a, 33)
		if !a.envHolding {
			t.Errorf("shape $%02X: envHolding=false after 33 ticks — should hold per CONTINUE=0",
				shape)
		}
		if got := a.envelopeLevel(); got != 0 {
			t.Errorf("shape $%02X: end level = %d, want 0", shape, got)
		}
	}
}

// TestEnvelope_Shape9_HoldAtZero — 1001 = continue + hold + decay.
func TestEnvelope_Shape9_HoldAtZero(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x09)
	runEnvCycles(a, 33)
	if !a.envHolding {
		t.Error("shape $09: envHolding=false after cycle")
	}
	if got := a.envelopeLevel(); got != 0 {
		t.Errorf("shape $09: end level = %d, want 0 (\\____)", got)
	}
}

// TestEnvelope_Shape11_HoldAtFifteen — 1011 = continue + hold + alt + decay.
// Decay then hold at 15 (the "_" inverts to "¯" because of alt).
func TestEnvelope_Shape11_HoldAtFifteen(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0B)
	runEnvCycles(a, 33)
	if !a.envHolding {
		t.Error("shape $0B: envHolding=false after cycle")
	}
	if got := a.envelopeLevel(); got != 15 {
		t.Errorf("shape $0B: end level = %d, want 15 (\\¯¯¯¯)", got)
	}
}

// TestEnvelope_Shape13_HoldAtFifteen — 1101 = continue + hold + attack.
// Attack then hold at 15.
func TestEnvelope_Shape13_HoldAtFifteen(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0D)
	runEnvCycles(a, 33)
	if !a.envHolding {
		t.Error("shape $0D: envHolding=false after cycle")
	}
	if got := a.envelopeLevel(); got != 15 {
		t.Errorf("shape $0D: end level = %d, want 15 (/¯¯¯¯)", got)
	}
}

// TestEnvelope_Shape15_HoldAtZero — 1111 = continue + hold + alt + attack.
// Attack-then-hold-at-0 per spec (alt inverts the hold end).
func TestEnvelope_Shape15_HoldAtZero(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0F)
	runEnvCycles(a, 33)
	if !a.envHolding {
		t.Error("shape $0F: envHolding=false after cycle")
	}
	if got := a.envelopeLevel(); got != 0 {
		t.Errorf("shape $0F: end level = %d, want 0 (/____)", got)
	}
}

// TestEnvelope_Shape8_RepeatingNeverHolds — 1000 = continue (no hold)
// = repeating sawtooth/triangle. envHolding must stay false across
// multiple cycles.
func TestEnvelope_Shape8_RepeatingNeverHolds(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x08)
	runEnvCycles(a, 100) // 3+ cycles
	if a.envHolding {
		t.Error("shape $08: envHolding=true — should never hold (CONTINUE=1, HOLD=0)")
	}
}

// TestEnvelope_Shape10_AlternatingNeverHolds — 1010 = alternating triangle.
func TestEnvelope_Shape10_AlternatingNeverHolds(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0A)
	runEnvCycles(a, 100)
	if a.envHolding {
		t.Error("shape $0A: envHolding=true — should never hold")
	}
}

// TestEnvelope_Shape12_RepeatingNeverHolds.
func TestEnvelope_Shape12_RepeatingNeverHolds(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0C)
	runEnvCycles(a, 100)
	if a.envHolding {
		t.Error("shape $0C: envHolding=true — should never hold")
	}
}

// TestEnvelope_Shape14_AlternatingNeverHolds.
func TestEnvelope_Shape14_AlternatingNeverHolds(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x0E)
	runEnvCycles(a, 100)
	if a.envHolding {
		t.Error("shape $0E: envHolding=true — should never hold")
	}
}

// TestEnvelope_LevelAtStart verifies the initial output level
// matches the spec: attack shapes start at 0, decay shapes start at 15.
func TestEnvelope_LevelAtStart(t *testing.T) {
	for _, tc := range []struct {
		shape byte
		want  byte
		name  string
	}{
		{0x00, 15, "shape $00 decay starts at 15"},
		{0x04, 0, "shape $04 attack starts at 0"},
		{0x08, 15, "shape $08 decay starts at 15"},
		{0x0C, 0, "shape $0C attack starts at 0"},
	} {
		a := New()
		a.WriteRegister(RegEnvShape, tc.shape)
		if got := a.envelopeLevel(); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestEnvelope_ReassertingShapeResetsHolding verifies that re-writing
// NR$13 unlocks a held envelope and re-starts it. NextZXOS / typical
// AY-using code does this to retrigger the envelope generator.
func TestEnvelope_ReassertingShapeResetsHolding(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvShape, 0x09) // decay-then-hold
	runEnvCycles(a, 33)
	if !a.envHolding {
		t.Fatal("setup: should be holding after one cycle")
	}
	// Re-write the same shape. startEnvelope must clear envHolding.
	a.WriteRegister(RegEnvShape, 0x09)
	if a.envHolding {
		t.Error("envHolding still set after shape re-write — startEnvelope should reset")
	}
}

// TestRegMasks_AllSixteen exhaustively verifies each register's
// mask: writing 0xFF should produce only the bits the chip
// physically supports.
func TestRegMasks_AllSixteen(t *testing.T) {
	cases := []struct {
		reg, mask byte
		name      string
	}{
		{RegToneALow, 0xFF, "ToneALow"},
		{RegToneAHigh, 0x0F, "ToneAHigh (4-bit coarse)"},
		{RegToneBLow, 0xFF, "ToneBLow"},
		{RegToneBHigh, 0x0F, "ToneBHigh"},
		{RegToneCLow, 0xFF, "ToneCLow"},
		{RegToneCHigh, 0x0F, "ToneCHigh"},
		{RegNoise, 0x1F, "Noise (5-bit)"},
		{RegMixer, 0xFF, "Mixer (8-bit, bits 6/7 = I/O direction)"},
		{RegVolumeA, 0x1F, "VolumeA (4 vol + 1 env)"},
		{RegVolumeB, 0x1F, "VolumeB"},
		{RegVolumeC, 0x1F, "VolumeC"},
		{RegEnvLow, 0xFF, "EnvLow"},
		{RegEnvHigh, 0xFF, "EnvHigh"},
		{RegEnvShape, 0x0F, "EnvShape (4-bit)"},
		{RegIOA, 0xFF, "IOA"},
		{RegIOB, 0xFF, "IOB"},
	}
	for _, c := range cases {
		a := New()
		a.WriteRegister(c.reg, 0xFF)
		got := a.ReadRegister(c.reg)
		if got != c.mask {
			t.Errorf("%s (reg %d): wrote 0xFF, read 0x%02X, want mask 0x%02X",
				c.name, c.reg, got, c.mask)
		}
	}
}

// TestToneAPeriod_16BitComposition — 16-bit tone period is
// composed of low + (high<<8); writes to high are masked to 4 bits.
// Period = 0 (all zeros) is a special case (acts as period 1) but
// reading should return what was written, masked.
func TestToneAPeriod_16BitComposition(t *testing.T) {
	a := New()
	a.WriteRegister(RegToneALow, 0xAB)
	a.WriteRegister(RegToneAHigh, 0xCD) // masked to $0D
	if got := a.ReadRegister(RegToneALow); got != 0xAB {
		t.Errorf("ToneALow = $%02X, want $AB", got)
	}
	if got := a.ReadRegister(RegToneAHigh); got != 0x0D {
		t.Errorf("ToneAHigh = $%02X, want $0D (masked)", got)
	}
}

// TestEnvPeriod_16BitWriteback — EnvLow + EnvHigh both store all 8
// bits unmasked (envelope period is full 16 bits).
func TestEnvPeriod_16BitWriteback(t *testing.T) {
	a := New()
	a.WriteRegister(RegEnvLow, 0x55)
	a.WriteRegister(RegEnvHigh, 0xAA)
	if got := a.ReadRegister(RegEnvLow); got != 0x55 {
		t.Errorf("EnvLow = $%02X, want $55", got)
	}
	if got := a.ReadRegister(RegEnvHigh); got != 0xAA {
		t.Errorf("EnvHigh = $%02X, want $AA", got)
	}
}

// TestMixer_DefaultMutesAll — fresh AY mixer is $3F = all channels
// off (bits 0..2 = tone disable, bits 3..5 = noise disable, both set).
func TestMixer_DefaultMutesAll(t *testing.T) {
	a := New()
	if got := a.ReadRegister(RegMixer); got != 0x3F {
		t.Errorf("default Mixer = $%02X, want $3F (all muted)", got)
	}
}

// TestVolumeRegisterEnvelopeBit — bit 4 of volume reg selects
// envelope-mode (= channel uses envelope generator volume).
func TestVolumeRegisterEnvelopeBit(t *testing.T) {
	a := New()
	// $10 = envelope bit set, fixed volume = 0.
	a.WriteRegister(RegVolumeA, 0x10)
	if got := a.ReadRegister(RegVolumeA); got != 0x10 {
		t.Errorf("VolumeA with env bit: read $%02X, want $10", got)
	}
	// $1F = envelope + max fixed volume (the envelope bit wins).
	a.WriteRegister(RegVolumeA, 0x1F)
	if got := a.ReadRegister(RegVolumeA); got != 0x1F {
		t.Errorf("VolumeA full: read $%02X, want $1F", got)
	}
}
