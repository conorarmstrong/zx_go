package saa1099

// Datasheet-faithful test suite for the Philips SAA1099 "Microprocessor
// Controlled Stereo Sound Generator". The oracle throughout is the Philips
// SAA1099 datasheet (no FPGA core exists for this part). Behaviour cited:
//
//   - Register map: amplitude 00h-05h, frequency 08h-0Dh, octave 10h-12h,
//     frequency-enable 14h, noise-enable 15h, noise-generator 16h,
//     envelope-generator 18h/19h, sound-enable/reset 1Ch.
//   - Tone frequency f = (clk/512)·2^octave / (511 − freq); with the SAM's
//     8 MHz oscillator clk/512 = 15625, giving the datasheet's 31 Hz..7.81 kHz.
//   - Amplitude register: bits 0-3 = left level (0..15), bits 4-7 = right level.
//   - Noise generator rates (16h, gen0 bits 0-1, gen1 bits 4-5):
//     0 = clk/256, 1 = clk/512, 2 = clk/1024, 3 = tied to the group tone gen.
//   - Envelope register (18h/19h): bit0 = right-inverts-left, bits1-3 = shape,
//     bit4 = resolution (set ⇒ 3-bit, LSB masked), bit5 = clock source
//     (clear ⇒ internal FG1/FG4, set ⇒ external), bit7 = envelope enable.
//     Shapes 0-7: 0 zero, 1 max, 2 single decay, 3 repetitive decay,
//     4 single triangle, 5 repetitive triangle, 6 single attack,
//     7 repetitive attack.
//   - Control register 1Ch: bit0 = sound enable (all channels), bit1 = reset
//     and synchronise the generators.

import (
	"math"
	"testing"
)

// --- Register map / write protocol --------------------------------------

// TestRegisterMapDecode pins the address-latch + data protocol across every
// documented register block, and confirms address wrapping is mod-32.
func TestRegisterMapDecode(t *testing.T) {
	s := New()
	cases := []struct {
		reg  byte
		name string
	}{
		{0x00, "amp0"}, {0x05, "amp5"},
		{0x08, "freq0"}, {0x0D, "freq5"},
		{0x10, "octave01"}, {0x12, "octave45"},
		{0x14, "freqEnable"}, {0x15, "noiseEnable"}, {0x16, "noiseGen"},
		{0x18, "env0"}, {0x19, "env1"}, {0x1C, "control"},
	}
	for _, c := range cases {
		s.WriteAddress(c.reg)
		s.WriteData(0x5A)
		if got := s.ReadRegister(c.reg); got != 0x5A {
			t.Errorf("%s (%#02x): wrote 0x5A, read %#02x", c.name, c.reg, got)
		}
	}
	// The SAA1099 has 32 register addresses; the latch is 5-bit. Address 0x28
	// must alias to 0x08.
	s.WriteAddress(0x28)
	s.WriteData(0x33)
	if got := s.ReadRegister(0x08); got != 0x33 {
		t.Errorf("address latch must be mod-32: 0x28 should alias 0x08, got %#02x", got)
	}
}

// --- Tone frequency divider math ----------------------------------------

// TestToneDividerMath pins f = 15625·2^octave / (511 − freq) for several
// (octave, freq) pairs, and verifies the datasheet's documented endpoints:
// octave 0 / freq 0 ⇒ ~31 Hz, octave 7 / freq 255 ⇒ 7.8125 kHz.
func TestToneDividerMath(t *testing.T) {
	s := New()
	check := func(octave, freq int, want float64) {
		t.Helper()
		s.Reset()
		s.WriteRegister(regFreq0, byte(freq))
		s.WriteRegister(regOctave0, byte(octave)) // ch0 octave in low nibble
		if got := s.ToneFrequency(0); math.Abs(got-want) > 0.01 {
			t.Errorf("octave=%d freq=%d: f=%.4f, want %.4f", octave, freq, got, want)
		}
	}
	check(0, 0, 15625.0/511.0)              // ≈ 30.578 Hz (datasheet ~31 Hz floor)
	check(7, 255, 15625.0*128.0/256.0)      // 7812.5 Hz (datasheet 7.81 kHz ceiling)
	check(4, 0, 15625.0*16.0/511.0)         // mid-range pair
	check(3, 0x80, 15625.0*8.0/(511.0-128)) // arbitrary pair
}

// TestOctavePacking confirms 10h-12h pack two channels per byte: the even
// channel in bits 0-2, the odd channel in bits 4-6.
func TestOctavePacking(t *testing.T) {
	s := New()
	s.WriteRegister(regOctave0+0, 0x52) // ch0=oct2, ch1=oct5
	s.WriteRegister(regOctave0+1, 0x31) // ch2=oct1, ch3=oct3
	s.WriteRegister(regOctave0+2, 0x70) // ch4=oct0, ch5=oct7
	want := []int{2, 5, 1, 3, 0, 7}
	for c, w := range want {
		if got := s.octave(c); got != w {
			t.Errorf("octave(ch%d) = %d, want %d", c, got, w)
		}
	}
}

// --- Amplitude → per-channel left/right mapping -------------------------

// TestAmplitudeNibbleMapping drives the amplitude register and checks the low
// nibble feeds the left output and the high nibble the right output,
// independently, across all six channels.
func TestAmplitudeNibbleMapping(t *testing.T) {
	measure := func(amp byte) (left, right int) {
		s := New()
		s.WriteRegister(regFreq0, 0x00)
		s.WriteRegister(regOctave0, 0x00) // lowest freq ⇒ a long high half-period
		s.WriteRegister(regAmp0, amp)
		s.WriteRegister(regFreqEn, 0x01) // ch0 tone on
		s.WriteRegister(regMaster, 0x01)
		buf := make([]int16, 64*2)
		s.GenerateStereo(buf)
		// The very first sample is in the tone's high half (phase < 0.5).
		return int(buf[0]), int(buf[1])
	}
	// Left only.
	if l, r := measure(0x0F); l == 0 || r != 0 {
		t.Errorf("amp=0x0F (L=15,R=0): L=%d R=%d, want L>0 R=0", l, r)
	}
	// Right only.
	if l, r := measure(0xF0); l != 0 || r == 0 {
		t.Errorf("amp=0xF0 (L=0,R=15): L=%d R=%d, want L=0 R>0", l, r)
	}
	// Both, and the high nibble (right) at half level should be < full left.
	lFull, _ := measure(0x0F)
	_, rHalf := measure(0x70)
	if rHalf <= 0 || rHalf >= lFull {
		t.Errorf("amp=0x70: right(7) = %d should be between 0 and full-left(15) = %d", rHalf, lFull)
	}
}

// --- Frequency-enable & noise-enable ------------------------------------

// TestFrequencyEnableGate confirms reg 14h bit n gates channel n's tone into
// the mix; a disabled tone (with no noise) is silent.
func TestFrequencyEnableGate(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0x80)
	s.WriteRegister(regOctave0, 0x04)
	s.WriteRegister(regAmp0, 0x0F)
	s.WriteRegister(regMaster, 0x01)
	buf := make([]int16, 882*2)

	s.WriteRegister(regFreqEn, 0x00) // ch0 tone off
	s.GenerateStereo(buf)
	if nonZero(buf) {
		t.Error("tone disabled in reg 14h must be silent")
	}
	s.WriteRegister(regFreqEn, 0x01) // ch0 tone on
	s.GenerateStereo(buf)
	if !nonZero(buf) {
		t.Error("tone enabled in reg 14h must be audible")
	}
}

// TestNoiseEnableGate confirms reg 15h bit n routes noise into channel n, and
// the per-channel routing is independent of the tone-enable bits.
func TestNoiseEnableGate(t *testing.T) {
	s := New()
	s.WriteRegister(regAmp0+3, 0x0F) // amplitude on channel 3 (noise group 1)
	s.WriteRegister(regNoise, 0x00)  // both gens 31.25 kHz
	s.WriteRegister(regMaster, 0x01)
	buf := make([]int16, 882*2)

	s.WriteRegister(regNoiseEn, 0x00) // ch3 noise off, no tone ⇒ silent
	s.GenerateStereo(buf)
	if nonZero(buf) {
		t.Error("noise disabled in reg 15h must be silent")
	}
	s.WriteRegister(regNoiseEn, 1<<3) // ch3 noise on (uses group-1 generator)
	s.GenerateStereo(buf)
	if !nonZero(buf) {
		t.Error("noise enabled in reg 15h must be audible")
	}
}

// --- Noise generator rates (reg 16h) ------------------------------------

// TestNoiseRateSelect pins the four documented noise rates per generator and
// the per-generator bitfield packing (gen0 bits 0-1, gen1 bits 4-5).
func TestNoiseRateSelect(t *testing.T) {
	s := New()
	// Generator 0 (low nibble).
	rates0 := map[byte]float64{
		0x00: MasterClock / 256.0,
		0x01: MasterClock / 512.0,
		0x02: MasterClock / 1024.0,
	}
	for code, want := range rates0 {
		s.WriteRegister(regNoise, code)
		if got := s.noiseRate(0); math.Abs(got-want) > 0.01 {
			t.Errorf("noise gen0 code %d: rate=%.2f, want %.2f", code, got, want)
		}
	}
	// Code 3 ⇒ tied to the group's tone generator (ch0 for gen0).
	s.WriteRegister(regFreq0, 0x00)
	s.WriteRegister(regOctave0, 0x05)
	s.WriteRegister(regNoise, 0x03)
	if got, want := s.noiseRate(0), s.ToneFrequency(0); math.Abs(got-want) > 0.01 {
		t.Errorf("noise gen0 code 3: rate=%.2f, want tone(ch0)=%.2f", got, want)
	}
	// Generator 1 lives in the high nibble; gen0 must be unaffected.
	s.WriteRegister(regNoise, 0x20) // gen1 = code 2 (clk/1024), gen0 = code 0
	if got, want := s.noiseRate(1), MasterClock/1024.0; math.Abs(got-want) > 0.01 {
		t.Errorf("noise gen1 code 2: rate=%.2f, want %.2f", got, want)
	}
	if got, want := s.noiseRate(0), MasterClock/256.0; math.Abs(got-want) > 0.01 {
		t.Errorf("noise gen0 (unset) should be code 0: rate=%.2f, want %.2f", got, want)
	}
}

// --- Envelope generator: enable & shapes --------------------------------

// TestEnvelopeEnableBit confirms bit 7 of 18h/19h enables the generator.
func TestEnvelopeEnableBit(t *testing.T) {
	s := New()
	s.WriteRegister(regEnv0, 1<<1) // shape 0, not enabled
	if s.envEnabled(0) {
		t.Error("envelope must be disabled with bit 7 clear")
	}
	s.WriteRegister(regEnv0, 0x80|(1<<1))
	if !s.envEnabled(0) {
		t.Error("envelope must be enabled with bit 7 set")
	}
}

// TestEnvelopeShapeMaxAndZero pins the two constant shapes: shape 1 holds 15,
// shape 0 holds 0.
func TestEnvelopeShapeMaxAndZero(t *testing.T) {
	s := New()
	s.WriteRegister(regEnv0, 0x80|(1<<1)) // shape 1 = maximum amplitude
	if got := s.envLevel(0); got != 15 {
		t.Errorf("shape 1 (max) level = %d, want 15", got)
	}
	s.WriteRegister(regEnv0, 0x80|(0<<1)) // shape 0 = zero amplitude
	if got := s.envLevel(0); got != 0 {
		t.Errorf("shape 0 (zero) level = %d, want 0", got)
	}
}

// TestEnvelopeDecayShape pins shape 2 (single decay): starts at 15 and steps
// down 15,14,...,0.
func TestEnvelopeDecayShape(t *testing.T) {
	s := New()
	s.WriteRegister(regEnv0, 0x80|(2<<1)) // shape 2 = single decay
	for step := 0; step <= 15; step++ {
		s.envStep[0] = step
		if got, want := s.envLevel(0), 15-step; got != want {
			t.Errorf("decay step %d: level=%d, want %d", step, got, want)
		}
	}
}

// TestEnvelopeTriangleShape pins shape 4 (single triangle): 0→15→0.
func TestEnvelopeTriangleShape(t *testing.T) {
	s := New()
	s.WriteRegister(regEnv0, 0x80|(4<<1)) // shape 4 = single triangle
	// Rising half.
	for step := 0; step <= 15; step++ {
		s.envStep[0] = step
		if got := s.envLevel(0); got != step {
			t.Errorf("triangle rising step %d: level=%d, want %d", step, got, step)
		}
	}
	// Falling half (step 16 → 15, step 31 → 0).
	for step := 16; step <= 31; step++ {
		s.envStep[0] = step
		if got, want := s.envLevel(0), 31-step; got != want {
			t.Errorf("triangle falling step %d: level=%d, want %d", step, got, want)
		}
	}
}

// TestEnvelopeAttackShape pins shape 6 (single attack): the level RISES
// 0,1,2,...,15.
func TestEnvelopeAttackShape(t *testing.T) {
	s := New()
	s.WriteRegister(regEnv0, 0x80|(6<<1)) // shape 6 = single attack
	for step := 0; step <= 15; step++ {
		s.envStep[0] = step
		if got := s.envLevel(0); got != step {
			t.Errorf("attack step %d: level=%d, want %d (rising ramp)", step, got, step)
		}
	}
}

// TestEnvelopeRepetitiveAttack pins shape 7 (repetitive attack): the rising
// ramp loops, so it never latches done — after a full cycle it is back near 0.
func TestEnvelopeRepetitiveAttack(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0+1, 0x00) // ch1 = the EG0 internal clock
	s.WriteRegister(regOctave0, 0x70) // ch1 octave high ⇒ fast clock
	s.WriteRegister(regEnv0, 0x80|(7<<1))
	s.WriteRegister(regMaster, 0x01)
	// Verify it ramps up and wraps (i.e. it is repetitive, not single-shot).
	start := s.envLevel(0)
	if start != 0 {
		t.Errorf("attack should start at 0, got %d", start)
	}
	buf := make([]int16, 4096*2)
	s.GenerateStereo(buf)
	if s.envDone[0] {
		t.Error("repetitive attack must never latch done")
	}
}

// TestEnvelopeSingleShotLatches confirms single shapes (2,4,6) latch at the
// end and stop, whereas repetitive shapes (3,5,7) keep cycling.
func TestEnvelopeSingleShotLatches(t *testing.T) {
	run := func(shape byte) bool {
		s := New()
		s.WriteRegister(regFreq0+1, 0x00)
		s.WriteRegister(regOctave0, 0x70) // fast EG clock
		s.WriteRegister(regEnv0, 0x80|(shape<<1))
		s.WriteRegister(regMaster, 0x01)
		buf := make([]int16, 8192*2)
		s.GenerateStereo(buf)
		return s.envDone[0]
	}
	for _, shape := range []byte{2, 4, 6} { // single decay/triangle/attack
		if !run(shape) {
			t.Errorf("single shape %d must latch done after one cycle", shape)
		}
	}
	for _, shape := range []byte{3, 5, 7} { // repetitive
		if run(shape) {
			t.Errorf("repetitive shape %d must not latch done", shape)
		}
	}
}

// --- Envelope resolution bit (bit 4) ------------------------------------

// TestEnvelopeResolutionBit pins bit 4: when set the generator runs at 3-bit
// resolution, i.e. the level's LSB is masked (even levels only).
func TestEnvelopeResolutionBit(t *testing.T) {
	s := New()
	// Shape 2 (decay) at step 0 ⇒ level 15 normally; 3-bit ⇒ 14.
	s.WriteRegister(regEnv0, 0x80|(1<<4)|(2<<1)) // enable + resolution + decay
	s.envStep[0] = 0
	if got := s.envLevel(0); got != 14 {
		t.Errorf("3-bit decay at step0: level=%d, want 14 (15 with LSB masked)", got)
	}
	s.envStep[0] = 2 // 4-bit ⇒ 13; 3-bit ⇒ 12
	if got := s.envLevel(0); got != 12 {
		t.Errorf("3-bit decay at step2: level=%d, want 12 (13 with LSB masked)", got)
	}
	// Without the resolution bit, the LSB survives.
	s.WriteRegister(regEnv0, 0x80|(2<<1))
	s.envStep[0] = 0
	if got := s.envLevel(0); got != 15 {
		t.Errorf("4-bit decay at step0: level=%d, want 15", got)
	}
}

// --- Envelope clock source (bit 5) --------------------------------------

// TestEnvelopeExternalClock pins bit 5: with the external-clock bit set, the
// internal tone-driven clock must NOT advance the envelope (the datasheet ties
// the envelope to the FBLNK/external pin instead, which the SAM does not feed).
func TestEnvelopeExternalClock(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0+1, 0x00)
	s.WriteRegister(regOctave0, 0x70) // fast internal clock if it were used
	// External clock (bit 5) + single decay shape.
	s.WriteRegister(regEnv0, 0x80|(1<<5)|(2<<1))
	s.WriteRegister(regMaster, 0x01)
	first := s.envLevel(0)
	buf := make([]int16, 8192*2)
	s.GenerateStereo(buf)
	if s.envLevel(0) != first {
		t.Errorf("external-clock envelope must not advance on the internal clock: %d → %d", first, s.envLevel(0))
	}
}

// --- Channel→output routing via the envelope mirror bit -----------------

// TestEnvelopeMirrorBit pins bit 0: when clear, left and right share the
// envelope; when set, the right channel is the inverse (15 − left).
func TestEnvelopeMirrorBit(t *testing.T) {
	s := New()
	// Mirror off: both sides equal.
	s.WriteRegister(regEnv0, 0x80|(1<<1)) // shape 1 (max) ⇒ level 15
	if l, r := s.envApplied(0); l != 15 || r != 15 {
		t.Errorf("mirror off, max shape: L=%d R=%d, want 15/15", l, r)
	}
	// Mirror on: right = 15 − left.
	s.WriteRegister(regEnv0, 0x80|0x01|(1<<1)) // mirror + max
	if l, r := s.envApplied(0); l != 15 || r != 0 {
		t.Errorf("mirror on, max shape: L=%d R=%d, want 15/0", l, r)
	}
	// Mirror on, mid-level (shape 4 triangle at step 5 ⇒ 5).
	s.WriteRegister(regEnv0, 0x80|0x01|(4<<1))
	s.envStep[0] = 5
	if l, r := s.envApplied(0); l != 5 || r != 10 {
		t.Errorf("mirror on, level 5: L=%d R=%d, want 5/10", l, r)
	}
}

// TestEnvelopeRoutesToChannel2And5 confirms EG0 controls channel 2's amplitude
// and EG1 controls channel 5's (the datasheet ties each envelope to the third
// channel of its group).
func TestEnvelopeRoutesToChannel2And5(t *testing.T) {
	for _, tc := range []struct {
		ch  int
		env byte
	}{{2, regEnv0}, {5, regEnv1}} {
		s := New()
		s.WriteRegister(byte(regFreq0+tc.ch), 0x00)
		// Lowest octave ⇒ first sample sits in the tone's high half.
		s.WriteRegister(regFreqEn, 1<<uint(tc.ch))
		s.WriteRegister(byte(regAmp0+tc.ch), 0x00) // register amplitude 0
		s.WriteRegister(tc.env, 0x80|(1<<1))       // envelope shape 1 = max ⇒ 15
		s.WriteRegister(regMaster, 0x01)
		buf := make([]int16, 64*2)
		s.GenerateStereo(buf)
		if buf[0] == 0 {
			t.Errorf("ch%d: envelope (max) must drive amplitude despite amp reg = 0", tc.ch)
		}
	}
}

// --- Master enable / reset (reg 1Ch) ------------------------------------

// TestMasterEnableGate confirms bit 0 of 1Ch globally gates all output.
func TestMasterEnableGate(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0x80)
	s.WriteRegister(regOctave0, 0x04)
	s.WriteRegister(regAmp0, 0xFF)
	s.WriteRegister(regFreqEn, 0x3F)
	buf := make([]int16, 882*2)

	s.WriteRegister(regMaster, 0x00) // disabled
	s.GenerateStereo(buf)
	if nonZero(buf) {
		t.Error("master enable clear (1Ch bit0=0) must mute all channels")
	}
	s.WriteRegister(regMaster, 0x01) // enabled
	s.GenerateStereo(buf)
	if !nonZero(buf) {
		t.Error("master enable set (1Ch bit0=1) must un-mute")
	}
}

// TestMasterResetSyncsGenerators confirms bit 1 of 1Ch resets/synchronises the
// phase accumulators (the datasheet's reset-and-sync function).
func TestMasterResetSyncsGenerators(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0x80)
	s.WriteRegister(regOctave0, 0x05)
	s.WriteRegister(regAmp0, 0x0F)
	s.WriteRegister(regFreqEn, 0x01)
	s.WriteRegister(regMaster, 0x01)
	buf := make([]int16, 256*2)
	s.GenerateStereo(buf) // advance the phases
	if s.tonePhase[0] == 0 {
		t.Fatal("phase should have advanced before reset")
	}
	s.WriteRegister(regMaster, 0x02) // reset/sync bit
	if s.tonePhase[0] != 0 || s.noisePhase[0] != 0 || s.envPhase[0] != 0 {
		t.Errorf("reset/sync (1Ch bit1) must zero the phase accumulators: tone=%v noise=%v env=%v",
			s.tonePhase[0], s.noisePhase[0], s.envPhase[0])
	}
}
