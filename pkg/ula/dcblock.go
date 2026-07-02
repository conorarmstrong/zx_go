package ula

// dcBlocker is a one-pole high-pass (DC-blocking) filter that models the real
// Spectrum's capacitor-coupled audio output. On the hardware the beeper line
// drives the speaker and the tape/TV out through a coupling capacitor, so a
// *held* speaker level produces no sustained sound — the cone settles and only
// transitions push air. Our reconstruction renders the 1-bit speaker as a
// steady level (beeperLow/beeperHigh), so without this filter an idle speaker
// sits at a full-scale DC rail and every transition to/from it (power-on,
// reset, the gaps between loader blocks) is a large step — the "speaker wired
// to a battery" click.
//
// The filter is the textbook DC blocker
//
//	y[n] = x[n] - x[n-1] + R·y[n-1]
//
// whose corner is fs·(1-R)/(2π). With R below the corner sits a few Hz — well
// under the lowest beeper/AY tone — so all musical content passes untouched
// while DC and the sub-audible thump are removed. Its step response is the
// step height itself (a full beeperLow→beeperHigh swing → 2·amplitude), which
// is why the beeper amplitude is kept low enough that 2·amplitude stays inside
// int16: an isolated toggle is then a clean transient, not a clipped spike.
type dcBlocker struct {
	prevIn  int32
	prevOut float64
	// limit clamps the output magnitude to the speaker's physical amplitude.
	// A high-pass's step response is the step height, so an isolated full
	// toggle (beeperLow→beeperHigh) would overshoot to 2·amplitude — a spike
	// louder than the level itself. The cone can't deflect past its drive
	// level, so the output is bounded to it. <=0 means int16 max (no extra clamp).
	limit  int32
	seeded bool
}

// dcBlockerR is the feedback coefficient. At 44.1 kHz, 0.9998 puts the
// high-pass corner near 1.4 Hz (≈110 ms time constant). It must sit well
// below the lowest audible beeper note: a higher corner (e.g. the original
// 0.999 ≈ 7 Hz) visibly droops the flat tops of low-frequency beeper square
// waves, turning the tone into a decaying ramp — which garbles beeper-engine
// music (Ghouls 'n' Ghosts plays its in-game music on the beeper). At 1.4 Hz
// the sustained DC rail (idle/boot/reset) is still removed while audible
// content passes essentially untouched.
const dcBlockerR = 0.9998

// process high-pass-filters samples in place. The first sample after a reset
// lazily seeds the filter from the current level, so a steady power-on/reset
// rail yields 0 immediately (no synthetic startup edge) and matches the audio
// system's prefill silence.
func (d *dcBlocker) process(samples []int16) {
	lim := d.limit
	if lim <= 0 || lim > 32767 {
		lim = 32767
	}
	limF := float64(lim)
	for i, s := range samples {
		x := int32(s)
		if !d.seeded {
			d.prevIn = x
			d.prevOut = 0
			d.seeded = true
		}
		// Keep the filter state (prevOut) un-clamped so the math stays linear
		// and a held level still decays correctly; only the emitted sample is
		// bounded to the speaker amplitude.
		y := float64(x-d.prevIn) + dcBlockerR*d.prevOut
		d.prevIn = x
		d.prevOut = y
		switch {
		case y > limF:
			samples[i] = int16(lim)
		case y < -limF:
			samples[i] = int16(-lim)
		default:
			samples[i] = int16(y)
		}
	}
}

// reset re-arms the lazy seed so the next frame's first sample establishes a
// fresh 0 baseline. Called on machine reset, where the audio queue is also
// re-primed with silence.
func (d *dcBlocker) reset() {
	d.seeded = false
	d.prevIn = 0
	d.prevOut = 0
}
