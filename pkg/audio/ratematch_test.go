package audio

import "testing"

// The emulator produces one frame of audio per emulated video frame, and it
// paces those frames to the model's real period: 50.08 Hz on a 48K, 50.02 Hz
// on the 128K family, 48.83 Hz on a Pentagon. The sound card drains at exactly
// SampleRate. A 48K therefore hands over about 70 more samples a second than
// the card plays. With nothing coupling the two clocks the ring filled up,
// latency climbed to the whole ring, and the overflow then cut samples out in
// bursts, audible as tearing on sustained notes (issue #12). A Pentagon ran the
// other way and underran constantly.

// simulateRate drives the ring the way the real goroutines do, on a simulated
// clock: the producer pushes SamplesPerFrame frames every 1/producerHz
// seconds, and the consumer pulls BufferSize frames every BufferSize/SampleRate
// seconds. It reports the lowest and highest fill seen at a pull (in stereo
// frames) after the warm-up, and how many pulls found the ring empty.
func simulateRate(t *testing.T, as *AudioSystem, producerHz float64, seconds float64) (minFill, maxFill, starved int) {
	t.Helper()
	const warmup = 30.0 // seconds for the controller to settle
	frame := make([]int16, SamplesPerFrame)
	out := make([]int16, BufferSize*ChannelCount)

	pushEvery := 1 / producerHz
	pullEvery := float64(BufferSize) / SampleRate
	nextPush, nextPull := 0.0, 0.0
	minFill, maxFill = queueCapacity, 0
	for nextPull < seconds {
		if nextPush <= nextPull {
			as.PushBeeperSamples(frame)
			nextPush += pushEvery
			continue
		}
		fill := as.queueSize / ChannelCount
		if nextPull > warmup {
			if fill < minFill {
				minFill = fill
			}
			if fill > maxFill {
				maxFill = fill
			}
			if fill < BufferSize {
				starved++
			}
		}
		as.popStereoSamples(out)
		nextPull += pullEvery
	}
	return minFill, maxFill, starved
}

// Every model's frame rate must be absorbed: after settling, the ring never
// overflows, never starves, and holds near its target latency.
func TestRateMatchingHoldsLatencyForEveryModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		hz   float64
	}{
		{"48K", 3_500_000.0 / 69888},
		{"128K", 3_546_900.0 / 70908},
		{"Pentagon", 3_500_000.0 / 71680},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := fakeSystem()
			as.prefillSilence()
			as.SetProducerFrameRate(tc.hz)

			minFill, maxFill, starved := simulateRate(t, as, tc.hz, 600)

			if starved != 0 {
				t.Errorf("%d pulls found less than a buffer queued: the ring underran", starved)
			}
			// Full ring means the overflow path dropped samples.
			if maxFill >= queueCapacity/ChannelCount-SamplesPerFrame {
				t.Errorf("fill reached %d of %d frames: the ring is filling up, latency grows and overflow tears",
					maxFill, queueCapacity/ChannelCount)
			}
			target := queuePrefill / ChannelCount
			if minFill < target/2 || maxFill > target*2 {
				t.Errorf("fill wandered to [%d, %d] frames, want it held near the %d-frame target",
					minFill, maxFill, target)
			}
		})
	}
}

// The host's timer and its sound card run off different crystals, so even a
// correctly stated producer rate is off by a fraction of a percent in practice.
// The fill-level feedback must absorb that drift on its own.
func TestRateMatchingAbsorbsHostClockDrift(t *testing.T) {
	for _, drift := range []float64{-0.003, 0.003} {
		as := fakeSystem()
		as.prefillSilence()
		as.SetProducerFrameRate(50) // what we believe

		minFill, maxFill, starved := simulateRate(t, as, 50*(1+drift), 600)

		target := queuePrefill / ChannelCount
		if starved != 0 || minFill < target/2 || maxFill > target*2 {
			t.Errorf("drift %+.1f%%: fill [%d, %d], %d starved pulls; want it held near %d frames",
				drift*100, minFill, maxFill, starved, target)
		}
	}
}

// When the producer runs slower than the card, consuming it slower means
// reading between its samples. The resampler must interpolate there, not
// repeat samples, or a steady tone would pick up a buzz at the beat rate.
func TestRateMatchingInterpolatesBetweenSamples(t *testing.T) {
	as := fakeSystem()
	as.SetProducerFrameRate(25) // half rate: each input spans two outputs

	in := make([]int16, 64)
	for i := range in {
		in[i] = int16(i * 100)
	}
	as.PushBeeperSamples(in)

	out := make([]int16, 20*ChannelCount)
	as.popStereoSamples(out)

	// The fill controller may trim the step by a fraction of a percent, so
	// allow a little slack; a repeated sample would be 50 away from the ramp.
	for i := 0; i < 20; i++ {
		want := float64(i) * 50
		if got := float64(out[i*2]); got < want-15 || got > want+15 {
			t.Fatalf("output %d = %v, want about %v (linear between inputs)", i, got, want)
		}
		if out[i*2] != out[i*2+1] {
			t.Fatalf("output %d: channels differ (%d, %d) for a mono input", i, out[i*2], out[i*2+1])
		}
	}
}

// Interpolating across a full-scale edge must not wrap. The difference between
// two int16 samples can exceed the int16 range, and a beeper edge is exactly
// that case.
func TestRateMatchingInterpolatesAcrossFullScaleEdges(t *testing.T) {
	as := fakeSystem()
	as.SetProducerFrameRate(25)
	as.PushBeeperSamples([]int16{-30000, 30000, 30000, 30000})

	out := make([]int16, 3*ChannelCount)
	as.popStereoSamples(out)

	if mid := out[2]; mid < -500 || mid > 500 {
		t.Errorf("halfway across a -30000 to 30000 edge = %d, want about 0", mid)
	}
}
