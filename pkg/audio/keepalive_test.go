package audio

import "testing"

// TestKeepAliveDitherBreaksSilence verifies the keep-alive dither turns a
// silent buffer into a continuous, very-low-level, mostly-non-zero signal.
// Sustained digital silence (long runs of exact 0) is what lets the macOS
// speaker amp power down and then pop when sound resumes; a sprinkling of
// sub-audible noise keeps it awake. The dither must stay within ±amp so it's
// inaudible.
func TestKeepAliveDitherBreaksSilence(t *testing.T) {
	buf := make([]int16, 1000) // silent
	rng := uint32(0x12345678)
	applyKeepAliveDither(buf, 16, &rng)

	nonzero := 0
	for i, s := range buf {
		if s < -16 || s > 16 {
			t.Fatalf("dither sample %d = %d, out of ±16 bound (would be audible)", i, s)
		}
		if s != 0 {
			nonzero++
		}
	}
	// Overwhelmingly non-zero: no sustained silence for the amp to sleep on.
	if nonzero < 900 {
		t.Errorf("only %d/1000 samples non-zero; dither too sparse to keep the amp awake", nonzero)
	}
}

// TestKeepAliveDitherDisabled verifies amp<=0 leaves the buffer untouched, so
// the feature can be switched off cleanly.
func TestKeepAliveDitherDisabled(t *testing.T) {
	buf := make([]int16, 8) // silent
	rng := uint32(1)
	applyKeepAliveDither(buf, 0, &rng)
	for i, s := range buf {
		if s != 0 {
			t.Errorf("amp=0 must not modify the buffer; sample %d = %d", i, s)
		}
	}
}

// TestKeepAliveDitherAddsToContent verifies the dither is added to existing
// audio (not replacing it) and stays bounded, so loud content is unaffected
// beyond an inaudible noise floor.
func TestKeepAliveDitherAddsToContent(t *testing.T) {
	buf := make([]int16, 100)
	for i := range buf {
		buf[i] = 5000
	}
	rng := uint32(99)
	applyKeepAliveDither(buf, 16, &rng)
	for i, s := range buf {
		if s < 5000-16 || s > 5000+16 {
			t.Errorf("sample %d = %d, want 5000±16 (dither added to content)", i, s)
		}
	}
}
