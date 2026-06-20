package saa1099

import (
	"math"
	"testing"
)

func nonZero(buf []int16) bool {
	for _, s := range buf {
		if s != 0 {
			return true
		}
	}
	return false
}

func TestWriteProtocol(t *testing.T) {
	s := New()
	s.WriteAddress(regAmp0 + 3)
	s.WriteData(0xA5)
	if got := s.ReadRegister(regAmp0 + 3); got != 0xA5 {
		t.Errorf("address-latch + data write: reg 0x03 = %#02x, want 0xA5", got)
	}
}

func TestToneFrequency(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0)   // channel 0 freq
	s.WriteRegister(regOctave0, 4) // channel 0 octave = 4
	want := float64(toneBase) * 16 / 511
	if got := s.ToneFrequency(0); math.Abs(got-want) > 0.01 {
		t.Errorf("ToneFrequency = %.2f, want %.2f", got, want)
	}
	// Octave packing: channel 1 uses the high nibble of reg 0x10.
	s.WriteRegister(regOctave0, 0x50) // ch0 oct 0, ch1 oct 5
	if s.octave(0) != 0 || s.octave(1) != 5 {
		t.Errorf("octave packing: ch0=%d ch1=%d, want 0/5", s.octave(0), s.octave(1))
	}
}

func TestSilentUntilEnabled(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0x80)
	s.WriteRegister(regOctave0, 4)
	s.WriteRegister(regAmp0, 0xFF)   // full L+R
	s.WriteRegister(regFreqEn, 0x01) // channel 0 tone on
	buf := make([]int16, 882*2)
	s.GenerateStereo(buf)
	if nonZero(buf) {
		t.Error("output must be silent until the master enable bit is set")
	}
	s.WriteRegister(regMaster, 0x01) // enable
	s.GenerateStereo(buf)
	if !nonZero(buf) {
		t.Error("enabled channel should produce output")
	}
}

func TestStereoAmplitude(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0, 0x80)
	s.WriteRegister(regOctave0, 3)
	s.WriteRegister(regAmp0, 0x0F)   // left only (low nibble), right = 0
	s.WriteRegister(regFreqEn, 0x01) // tone on
	s.WriteRegister(regMaster, 0x01)
	buf := make([]int16, 882*2)
	s.GenerateStereo(buf)
	var l, r int
	for i := 0; i+1 < len(buf); i += 2 {
		if buf[i] != 0 {
			l++
		}
		if buf[i+1] != 0 {
			r++
		}
	}
	if l == 0 {
		t.Error("left channel (amp low nibble) should be audible")
	}
	if r != 0 {
		t.Error("right channel (amp high nibble = 0) should be silent")
	}
}

func TestNoiseProducesOutput(t *testing.T) {
	s := New()
	s.WriteRegister(regAmp0, 0x0F)    // left amp
	s.WriteRegister(regNoiseEn, 0x01) // channel 0 noise on
	s.WriteRegister(regNoise, 0x00)   // 31.25 kHz
	s.WriteRegister(regMaster, 0x01)
	buf := make([]int16, 882*2)
	s.GenerateStereo(buf)
	if !nonZero(buf) {
		t.Error("noise-enabled channel should produce output")
	}
}

func TestEnvelopeDecays(t *testing.T) {
	s := New()
	s.WriteRegister(regFreq0+1, 0x00)     // channel 1 (envelope clock) freq
	s.WriteRegister(regOctave0, 0x77)     // fast clock so the envelope steps quickly
	s.WriteRegister(regEnv0, 0x80|(3<<1)) // enable + repetitive decay
	if !s.envEnabled(0) {
		t.Fatal("envelope 0 should be enabled")
	}
	first := s.envLevel(0) // step 0 → 15
	if first != 15 {
		t.Errorf("decay envelope should start at 15, got %d", first)
	}
	// Advance several thousand samples; the level must change (decay).
	buf := make([]int16, 4096*2)
	s.WriteRegister(regMaster, 0x01)
	s.GenerateStereo(buf)
	if s.envLevel(0) == first {
		t.Error("decay envelope level should have changed over time")
	}
}

func TestResetSilences(t *testing.T) {
	s := New()
	s.WriteRegister(regAmp0, 0xFF)
	s.WriteRegister(regFreqEn, 0x3F)
	s.WriteRegister(regMaster, 0x01)
	s.Reset()
	buf := make([]int16, 882*2)
	s.GenerateStereo(buf)
	if nonZero(buf) {
		t.Error("Reset should silence the chip")
	}
}
