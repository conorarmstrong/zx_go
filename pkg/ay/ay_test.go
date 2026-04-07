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
