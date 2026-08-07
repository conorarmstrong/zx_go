package main

import (
	"testing"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The emulation loop used to be driven by a flat 20 ms time.Ticker for every
// machine. No Sinclair model actually runs at 50.000 Hz, so every model was
// paced wrong, and a tick the host was too slow to service was silently lost
// with nothing to correct for it. These tests pin the model-derived frame
// period and the drift-free pacer that replaced it.

// TestFrameDurationMatchesModelFrameLength pins the wall-clock frame period
// against the model's own frame length and CPU clock, rather than a
// hardcoded 50 Hz.
func TestFrameDurationMatchesModelFrameLength(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		want  time.Duration
	}{
		// 69888 T at 3.5 MHz = 19.968 ms (50.08 Hz).
		{"48K", roms.Model48K, 19968 * time.Microsecond},
		// 70908 T at 3.5469 MHz = 19.99216 ms (50.02 Hz).
		{"128K", roms.Model128K, 19991541 * time.Nanosecond},
		{"+2", roms.ModelPlus2, 19991541 * time.Nanosecond},
		{"+3", roms.ModelPlus3, 19991541 * time.Nanosecond},
		{"Next", roms.ModelNext, 19991541 * time.Nanosecond},
		// 71680 T at 3.5 MHz = 20.48 ms (48.83 Hz).
		{"Pentagon", roms.ModelPentagon, 20480 * time.Microsecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := frameDurationForModel(tc.model)
			// Allow a nanosecond of integer-division slack.
			if d := got - tc.want; d > time.Nanosecond || d < -time.Nanosecond {
				t.Errorf("frameDurationForModel = %v, want %v", got, tc.want)
			}
			if got == 20*time.Millisecond {
				t.Errorf("%s is still paced at a flat 50 Hz", tc.name)
			}
		})
	}
}

// TestFramePacerHoldsCadenceWithoutDrift pins that servicing frames exactly
// on time keeps the deadlines exactly one period apart. A naive
// "sleep(period) each iteration" accumulates the service time as drift.
func TestFramePacerHoldsCadenceWithoutDrift(t *testing.T) {
	const period = 20 * time.Millisecond
	start := time.Unix(0, 0)

	p := newFramePacer(start, period)
	now := start
	for i := 1; i <= 100; i++ {
		// Each frame takes 3 ms of real work before the pacer is consulted.
		now = now.Add(3 * time.Millisecond)
		wait := p.next(now, period)
		now = now.Add(wait)
		if want := start.Add(time.Duration(i) * period); !now.Equal(want) {
			t.Fatalf("frame %d lands at %v, want %v (drift %v)",
				i, now.Sub(start), want.Sub(start), now.Sub(want))
		}
	}
}

// TestFramePacerSkipsMissedFrames pins the overrun behaviour: when the host
// falls behind, the pacer drops the frames it missed and re-anchors on the
// grid instead of queueing them up and running flat out to catch up.
func TestFramePacerSkipsMissedFrames(t *testing.T) {
	const period = 20 * time.Millisecond
	start := time.Unix(0, 0)
	p := newFramePacer(start, period)

	// A frame that took 3.5 periods to service.
	now := start.Add(70 * time.Millisecond)
	wait := p.next(now, period)
	if wait < 0 {
		t.Fatalf("wait = %v, want a non-negative delay", wait)
	}
	landed := now.Add(wait)
	if landed.Before(now) {
		t.Fatalf("next deadline %v is in the past (now %v)", landed, now)
	}
	// Re-anchored on the period grid, and strictly ahead of now.
	if off := landed.Sub(start) % period; off != 0 {
		t.Errorf("deadline %v is off the period grid by %v", landed.Sub(start), off)
	}
	if got, want := landed.Sub(start), 80*time.Millisecond; got != want {
		t.Errorf("deadline = %v, want %v", got, want)
	}
}

// TestFramePacerFollowsAModelSwitch pins that changing the period mid-run
// (the machine-switch menu) takes effect on the next frame rather than
// keeping the old cadence.
func TestFramePacerFollowsAModelSwitch(t *testing.T) {
	start := time.Unix(0, 0)
	p := newFramePacer(start, 20*time.Millisecond)

	now := start
	wait := p.next(now, 20*time.Millisecond)
	if wait != 20*time.Millisecond {
		t.Fatalf("first wait = %v, want 20ms", wait)
	}
	now = now.Add(wait)

	// Switch to the Pentagon's longer frame.
	wait = p.next(now, 20480*time.Microsecond)
	if wait != 20480*time.Microsecond {
		t.Errorf("wait after model switch = %v, want 20.48ms", wait)
	}
}
