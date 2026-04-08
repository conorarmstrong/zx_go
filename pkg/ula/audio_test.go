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

// TestFlushAudioFrameIntegratesSubSampleToggles is the regression
// test for the "fuzzy beep" fix. It drives several speaker toggles
// inside a single audio sample window and verifies the output sample
// reflects the *average* speaker level over the window, not just the
// state at one instant. Without integration the sample would jump
// between full high and full low depending on which toggle landed
// closest to the midpoint, producing pulse-width jitter.
func TestFlushAudioFrameIntegratesSubSampleToggles(t *testing.T) {
	const tpf = 69888
	tstatesPerSample := tpf / audio.SamplesPerFrame // ~79

	// Build events that toggle the speaker at 1/4 and 3/4 of the
	// way through sample 0's window. The speaker is therefore:
	//   - low for 25% of the window (state 0 → 25)
	//   - high for 50% of the window (25 → 75)
	//   - low for 25% of the window (75 → end)
	// Total high time = 50% → output should be exactly midway
	// between beeperLow and beeperHigh, i.e. 0.
	events := []audioEvent{
		{tstateOffset: tstatesPerSample / 4, state: true},
		{tstateOffset: 3 * tstatesPerSample / 4, state: false},
	}

	samples, _ := generateBeeperFrame(events, false)

	// Sample 0 should be near the midpoint between beeperLow and
	// beeperHigh. Without the integration fix, sample 0 would be
	// either beeperLow or beeperHigh (full ±6000). The integer
	// truncation of the event offsets means the duty cycle isn't
	// exactly 50% — typical result is ±100 — but anything inside
	// ±500 confirms the integration is averaging across the whole
	// window, not snapping to a single value.
	got := samples[0]
	if got < -500 || got > 500 {
		t.Errorf("sample 0 = %d, want near 0 (~50%% duty cycle averaged); a value near ±6000 means integration is broken", got)
	}
}

// TestFlushAudioFramePartialOverlap verifies the integration handles
// a transition that lands AT a sample boundary correctly — sample N
// should be 100% in the new state, sample N-1 should be 100% in the
// old state, with no smearing.
func TestFlushAudioFramePartialOverlap(t *testing.T) {
	const tpf = 69888
	const samples = audio.SamplesPerFrame
	// Toggle exactly at the boundary between sample 100 and sample 101.
	events := []audioEvent{
		{tstateOffset: 101 * tpf / samples, state: true},
	}
	out, _ := generateBeeperFrame(events, false)

	if out[100] != beeperLow {
		t.Errorf("sample 100 (just before boundary): got %d, want %d", out[100], beeperLow)
	}
	if out[101] != beeperHigh {
		t.Errorf("sample 101 (at boundary): got %d, want %d", out[101], beeperHigh)
	}
}

// newAudioTestULA returns a bare ULA struct suitable for the audio
// generator tests. It has no real memory or audio system attached;
// the tests only use the per-frame event slice.
func newAudioTestULA(t *testing.T) *ULA {
	t.Helper()
	return &ULA{}
}
