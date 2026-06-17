package ula

import "testing"

// TestDCBlockerSilencesIdleRail is the tracer for the captured bug: the
// beeper rests at a full-scale rail (beeperLow) whenever the speaker bit is
// idle, so the recorded output sat at -20000 DC for the whole session and
// every transition to/from that rail was a click. The real Spectrum's output
// is capacitor-coupled — a held level produces no sound. The DC blocker must
// turn a constant idle level into silence (0) with no startup step (it
// lazy-seeds from the first sample, matching the audio system's prefill 0).
func TestDCBlockerSilencesIdleRail(t *testing.T) {
	var d dcBlocker
	samples := make([]int16, 4000)
	for i := range samples {
		samples[i] = beeperLow // idle rail held for the whole stream
	}
	d.process(samples)

	if samples[0] != 0 {
		t.Errorf("first sample = %d, want 0 (lazy-seeded — no startup click)", samples[0])
	}
	for i, s := range samples {
		if s < -1 || s > 1 {
			t.Errorf("sample %d = %d, want ~0 (idle rail fully removed)", i, s)
			break
		}
	}
}

// TestDCBlockerClampsIsolatedToggleToSpeakerLevel pins the fix for the
// remaining click. A DC blocker's step response to an isolated full toggle
// (beeperLow -> beeperHigh) is the full swing height (2*amplitude) — a 32000
// spike for a single speaker bit flip, far louder than the bit's own level.
// The speaker can't physically deflect past its drive level, so the output is
// clamped to the speaker amplitude (beeperHigh): an isolated toggle reads as a
// click at the level, not a doubled spike.
func TestDCBlockerClampsIsolatedToggleToSpeakerLevel(t *testing.T) {
	d := dcBlocker{limit: int32(beeperHigh)}
	samples := make([]int16, 200)
	for i := range samples {
		if i >= 10 {
			samples[i] = beeperHigh // single rising edge from idle, held
		} else {
			samples[i] = beeperLow
		}
	}
	d.process(samples)

	peak := samples[10]
	if peak != beeperHigh {
		t.Errorf("isolated-toggle peak = %d, want %d (clamped to speaker level, not the 2x overshoot %d)",
			peak, beeperHigh, beeperHigh-beeperLow)
	}
}

// TestDCBlockerPreservesTone verifies an audible square-wave tone survives at
// near-full amplitude — the corner is only a few Hz, so a real beeper note
// must not be attenuated.
func TestDCBlockerPreservesTone(t *testing.T) {
	var d dcBlocker
	samples := make([]int16, 4410) // 0.1 s @ 44.1 kHz
	const half = 11                // ~2 kHz square wave
	for i := range samples {
		if (i/half)%2 == 0 {
			samples[i] = beeperHigh
		} else {
			samples[i] = beeperLow
		}
	}
	d.process(samples)

	var maxMag int16
	for i := len(samples) / 2; i < len(samples); i++ {
		m := samples[i]
		if m < 0 {
			m = -m
		}
		if m > maxMag {
			maxMag = m
		}
	}
	if maxMag < beeperHigh-beeperHigh/8 { // within ~12% of full amplitude
		t.Errorf("tone peak after DC block = %d, want ~%d (AC content preserved)", maxMag, beeperHigh)
	}
}

// TestIdleFrameProducesSilenceAfterDCBlock is the end-to-end boot/reset
// regression: an idle frame (no toggles) comes out of generateBeeperFrame as
// a full beeperLow DC rail; once DC-blocked it must be silence.
func TestIdleFrameProducesSilenceAfterDCBlock(t *testing.T) {
	var d dcBlocker
	samples, _ := generateBeeperFrame(nil, false) // all beeperLow
	d.process(samples)
	for i, s := range samples {
		if s < -1 || s > 1 {
			t.Errorf("idle sample %d = %d, want ~0 (no boot/reset click)", i, s)
			break
		}
	}
}
