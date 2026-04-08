package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/audio"
)

// TestFlushAudioFrameSilent verifies that a frame with no speaker
// toggles produces a constant DC level matching the initial speaker
// state — and crucially, exactly one frame's worth of samples.
func TestFlushAudioFrameSilent(t *testing.T) {
	u := newAudioTestULA(t)
	u.Speaker = false
	u.frameStartSpeakerState = false

	samples, _ := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)

	if len(samples) != audio.SamplesPerFrame {
		t.Fatalf("len(samples) = %d, want %d", len(samples), audio.SamplesPerFrame)
	}
	for i, s := range samples {
		if s != beeperLow {
			t.Errorf("sample %d = %d, want %d (silent low)", i, s, beeperLow)
			break
		}
	}
}

// TestFlushAudioFrameInitialHigh verifies that a "speaker held high
// for the whole frame" case produces all-high samples.
func TestFlushAudioFrameInitialHigh(t *testing.T) {
	u := newAudioTestULA(t)
	u.frameStartSpeakerState = true

	samples, _ := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)
	for i, s := range samples {
		if s != beeperHigh {
			t.Errorf("sample %d = %d, want %d (high)", i, s, beeperHigh)
			break
		}
	}
}

// TestFlushAudioFrameSquareWave drives a single mid-frame toggle and
// verifies the sample stream actually transitions where it should —
// the whole point of the rewrite. Before the fix this transition
// would have been collapsed away by the audio reader sampling once
// per buffer refill.
func TestFlushAudioFrameSquareWave(t *testing.T) {
	u := newAudioTestULA(t)
	u.frameStartSpeakerState = false
	// Toggle high at exactly the middle of the frame.
	u.audioEvents = []audioEvent{
		{tstateOffset: 69888 / 2, state: true},
	}

	samples, _ := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)

	// First sample must be low.
	if samples[0] != beeperLow {
		t.Errorf("sample 0 = %d, want %d", samples[0], beeperLow)
	}
	// Last sample must be high.
	if samples[len(samples)-1] != beeperHigh {
		t.Errorf("last sample = %d, want %d", samples[len(samples)-1], beeperHigh)
	}
	// Find the transition point and check it's near the middle.
	transition := -1
	for i := 1; i < len(samples); i++ {
		if samples[i-1] != samples[i] {
			transition = i
			break
		}
	}
	if transition < 0 {
		t.Fatal("no transition found in sample stream")
	}
	mid := audio.SamplesPerFrame / 2
	if transition < mid-2 || transition > mid+2 {
		t.Errorf("transition at sample %d, expected ~%d", transition, mid)
	}
}

// TestFlushAudioFrameMultipleToggles verifies several toggles per
// frame are reproduced in order. Drives a 4-step pattern across the
// frame and checks each segment has the expected level.
func TestFlushAudioFrameMultipleToggles(t *testing.T) {
	u := newAudioTestULA(t)
	u.frameStartSpeakerState = false
	const tpf = 69888
	u.audioEvents = []audioEvent{
		{tstateOffset: tpf / 4, state: true},
		{tstateOffset: tpf / 2, state: false},
		{tstateOffset: 3 * tpf / 4, state: true},
	}

	samples, _ := generateBeeperFrame(u.audioEvents, u.frameStartSpeakerState)

	// Sample at the middle of each quarter and check the level.
	check := func(t *testing.T, sampleIdx int, want int16, label string) {
		t.Helper()
		if samples[sampleIdx] != want {
			t.Errorf("%s (sample %d): got %d, want %d", label, sampleIdx, samples[sampleIdx], want)
		}
	}
	q := audio.SamplesPerFrame / 4
	check(t, q/2, beeperLow, "quarter 1 (low)")
	check(t, q+q/2, beeperHigh, "quarter 2 (high)")
	check(t, 2*q+q/2, beeperLow, "quarter 3 (low)")
	check(t, 3*q+q/2, beeperHigh, "quarter 4 (high)")
}

// newAudioTestULA returns a bare ULA struct suitable for the audio
// generator tests. It has no real memory or audio system attached;
// the tests only use the per-frame event slice.
func newAudioTestULA(t *testing.T) *ULA {
	t.Helper()
	return &ULA{}
}
