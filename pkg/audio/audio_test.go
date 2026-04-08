package audio

import (
	"testing"
)

// fakeSystem produces an AudioSystem instance that's safe to use
// without an oto context — we exercise the queue logic only, not the
// playback path.
func fakeSystem() *AudioSystem {
	return &AudioSystem{}
}

// TestPushAndPopRoundTrip verifies that samples pushed by the
// emulation goroutine come back out in order from the playback path.
func TestPushAndPopRoundTrip(t *testing.T) {
	as := fakeSystem()

	in := []int16{1, 2, 3, 4, 5}
	as.PushBeeperSamples(in)

	out := make([]int16, len(in))
	as.popBeeperSamples(out)

	for i := range in {
		if out[i] != in[i] {
			t.Errorf("sample %d: got %d, want %d", i, out[i], in[i])
		}
	}
}

// TestPopUnderflowHoldsLastSample verifies that when the queue is
// drained and more samples are requested than available, the unfilled
// slots hold the last delivered value (DC) rather than zero/garbage —
// this is what prevents the audio system from clicking on underrun.
func TestPopUnderflowHoldsLastSample(t *testing.T) {
	as := fakeSystem()
	as.PushBeeperSamples([]int16{100, 200, 300})

	out := make([]int16, 6)
	as.popBeeperSamples(out)

	want := []int16{100, 200, 300, 300, 300, 300} // last sample held
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("sample %d: got %d, want %d", i, out[i], want[i])
		}
	}
}

// TestPopFromEmptyHoldsZero verifies that the very first underrun
// (before any samples have been pushed) outputs zeros rather than
// garbage from uninitialised memory.
func TestPopFromEmptyHoldsZero(t *testing.T) {
	as := fakeSystem()
	out := make([]int16, 4)
	for i := range out {
		out[i] = 0x7FFF // poison
	}
	as.popBeeperSamples(out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("sample %d: got %d, want 0", i, v)
		}
	}
}

// TestQueueOverflowDropsOldest verifies that pushing more samples than
// the ring buffer can hold drops the oldest entries and keeps the
// newest, since the playback goroutine has fallen behind and we'd
// rather glitch than grow latency.
func TestQueueOverflowDropsOldest(t *testing.T) {
	as := fakeSystem()

	// Fill past capacity. queueCapacity is SamplesPerFrame*6 = 5292.
	// Push 7 frames (6174 samples) — the first frame should be
	// overwritten.
	const burst = SamplesPerFrame * 7
	in := make([]int16, burst)
	for i := range in {
		in[i] = int16(i % 32767)
	}
	as.PushBeeperSamples(in)

	// Drain everything and verify the first sample is from the
	// SECOND frame onwards (the oldest got dropped).
	out := make([]int16, queueCapacity)
	as.popBeeperSamples(out)

	expectedFirst := int16((burst - queueCapacity) % 32767)
	if out[0] != expectedFirst {
		t.Errorf("after overflow: out[0] = %d, want %d (oldest should be dropped)", out[0], expectedFirst)
	}
}

// TestResetClearsQueue verifies that Reset drops any buffered samples
// and resets the held-on-underflow value to silence.
func TestResetClearsQueue(t *testing.T) {
	as := fakeSystem()
	as.PushBeeperSamples([]int16{1, 2, 3})
	as.Reset()

	out := make([]int16, 3)
	for i := range out {
		out[i] = 0x7FFF // poison
	}
	as.popBeeperSamples(out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("after Reset: sample %d = %d, want 0", i, v)
		}
	}
}
