package main

import (
	"time"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// CPU clock rates in Hz. The 48K and the Pentagon run at 3.5 MHz; the 128K
// family (and the Next, which boots in 128K/+3 timing) at 3.5469 MHz.
const (
	cpuHz48K  = 3_500_000
	cpuHz128K = 3_546_900
)

// cpuHzForModel returns the model's CPU clock.
func cpuHzForModel(model roms.SpectrumModel) int {
	switch model {
	case roms.Model48K, roms.ModelPentagon:
		return cpuHz48K
	default:
		return cpuHz128K
	}
}

// frameDurationForModel returns the real wall-clock length of one video
// frame on the given model, derived from its frame length in T-states and
// its CPU clock rather than assumed to be 50 Hz:
//
//	48K       69888 T @ 3.5 MHz    = 19.968 ms  (50.08 Hz)
//	128K/+2/+3/Next
//	          70908 T @ 3.5469 MHz = 19.992 ms  (50.02 Hz)
//	Pentagon  71680 T @ 3.5 MHz    = 20.480 ms  (48.83 Hz)
//
// The emulation loop used a flat 20 ms tick for every machine, which is
// wrong for all of them and noticeably so for the Pentagon.
func frameDurationForModel(model roms.SpectrumModel) time.Duration {
	frameT := frameTStatesForModel(model)
	return time.Duration(frameT) * time.Second / time.Duration(cpuHzForModel(model))
}

// framePacer schedules emulated frames against a fixed wall-clock grid.
//
// Sleeping for one period per iteration accumulates each frame's service
// time as drift, and a plain time.Ticker silently drops the frames the host
// was too slow to service. The pacer instead tracks the deadline of the next
// frame: on-time frames stay exactly one period apart, and an overrun
// re-anchors to the next grid slot in the future, dropping the frames that
// were missed rather than running them back-to-back to catch up.
type framePacer struct {
	// last is the grid slot the previous frame was due at.
	last time.Time
}

// newFramePacer anchors the grid at start. The period is supplied per call
// to next, so a mid-run machine switch takes effect on the very next frame.
func newFramePacer(start time.Time) *framePacer {
	return &framePacer{last: start}
}

// next reports how long to wait before running the next frame, given the
// current time and the period now in force. The returned delay is never
// negative.
func (p *framePacer) next(now time.Time, period time.Duration) time.Duration {
	if period <= 0 {
		return 0
	}
	due := p.last.Add(period)
	if due.Before(now) {
		// Fell behind: skip the whole frames that were missed and re-anchor
		// on the next slot at or after now, so the backlog cannot compound
		// into a run-flat-out catch-up.
		skip := now.Sub(due)/period + 1
		due = due.Add(skip * period)
	}
	p.last = due
	return due.Sub(now)
}
