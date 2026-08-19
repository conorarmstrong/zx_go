package audio

// DCBlocker is a one-pole high-pass (DC-blocking) filter that models the
// capacitor-coupled audio output these machines have. On the hardware the
// speaker line drives the speaker and the tape/TV out through a capacitor, so a
// *held* speaker level produces no sustained sound — the cone settles and only
// transitions push air. Our reconstruction renders the 1-bit speaker as a
// steady level, so without this filter an idle speaker
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
// step height itself (a full low→high swing → 2·amplitude), which
// is why the beeper amplitude is kept low enough that 2·amplitude stays inside
// int16: an isolated toggle is then a clean transient, not a clipped spike.
type DCBlocker struct {
	prevIn  int32
	prevOut float64
	// limit clamps the output magnitude to the speaker's physical amplitude.
	// A high-pass's step response is the step height, so an isolated full
	// toggle (low→high) would overshoot to 2·amplitude — a spike
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

// Process high-pass-filters samples in place. The first sample after a reset
// lazily seeds the filter from the current level, so a steady power-on/reset
// rail yields 0 immediately (no synthetic startup edge) and matches the audio
// system's prefill silence.
func (d *DCBlocker) Process(samples []int16) {
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

// SetLimit bounds the output magnitude to the speaker's physical amplitude.
// <=0 means int16 max (no extra clamp).
func (d *DCBlocker) SetLimit(lim int32) { d.limit = lim }

// Reset re-arms the lazy seed so the next frame's first sample establishes a
// fresh 0 baseline. Called on machine reset, where the audio queue is also
// re-primed with silence.
func (d *DCBlocker) Reset() {
	d.seeded = false
	d.prevIn = 0
	d.prevOut = 0
}

// StereoDCBlocker is one filter per channel.
//
// The two cannot share a filter. A DCBlocker is a one-pole recurrence over
// consecutive samples, so running one across an interleaved buffer would
// high-pass every left sample against the previous RIGHT sample and vice
// versa. On a mono signal that happens to be harmless — the channels are
// equal, so the alternation is invisible — which is precisely why it would
// have survived every test the classic machines run and failed only on the one
// Next program that pans hard.
type StereoDCBlocker struct {
	l, r DCBlocker
}

// Left and Right expose the individual filters, so a caller capturing machine
// state can record each channel's position.
func (d *StereoDCBlocker) Left() *DCBlocker  { return &d.l }
func (d *StereoDCBlocker) Right() *DCBlocker { return &d.r }

// ProcessStereo high-pass-filters an interleaved buffer in place, each channel
// through its own filter. A trailing odd slot is left alone.
func (d *StereoDCBlocker) ProcessStereo(frame []int16) {
	for i := 0; i+1 < len(frame); i += 2 {
		d.l.Process(frame[i : i+1])
		d.r.Process(frame[i+1 : i+2])
	}
}

// SetLimit bounds both channels to the speaker's physical amplitude.
func (d *StereoDCBlocker) SetLimit(lim int32) {
	d.l.SetLimit(lim)
	d.r.SetLimit(lim)
}

// Limit reports the bound, which is the same on both channels.
func (d *StereoDCBlocker) Limit() int32 { return d.l.limit }

// Reset re-arms both channels.
func (d *StereoDCBlocker) Reset() {
	d.l.Reset()
	d.r.Reset()
}

// DCState is the wire form of one channel's filter position, for machines that
// capture their audio path. It lives beside the filter so that adding a field
// to DCBlocker and forgetting it is a change in one file rather than two.
type DCState struct {
	PrevIn  int32
	PrevOut float64
	Seeded  bool
}

// CaptureDC records a filter's position.
func CaptureDC(d *DCBlocker) DCState {
	return DCState{PrevIn: d.prevIn, PrevOut: d.prevOut, Seeded: d.seeded}
}

// ApplyDC restores a filter's position.
func ApplyDC(d *DCBlocker, s DCState) {
	d.prevIn, d.prevOut, d.seeded = s.PrevIn, s.PrevOut, s.Seeded
}
