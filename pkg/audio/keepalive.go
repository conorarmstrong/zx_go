package audio

// defaultKeepAliveLevel is the peak amplitude (16-bit units, out of 32767) of
// the keep-alive dither mixed into the output. It exists only to stop the
// macOS speaker amp powering down during silence and popping on the next
// sound. It is OFF by default: even a low-level continuous dither is audible
// as a faint hiss in quiet passages on sensitive setups, which is worse than
// an occasional hardware pop. Enable + tune it with --audio-keepalive-level
// (e.g. 16/24) if the startup/load pop bothers you. The content-side click
// fixes (no DC rail, clamped transients, decayed underruns) don't depend on
// this.
const defaultKeepAliveLevel int16 = 0

// applyKeepAliveDither adds a per-sample pseudo-random dither in [-amp, amp]
// to buf in place, advancing rng (a non-zero xorshift32 state). amp<=0 is a
// no-op. The dither is broadband (noise, not a tone) so it's masked by any
// real content and reads as an inaudible noise floor during silence — never a
// pitch or a sub-bass thump. It exists purely to keep the host audio device
// awake; the emulated Spectrum audio is unchanged.
func applyKeepAliveDither(buf []int16, amp int16, rng *uint32) {
	if amp <= 0 {
		return
	}
	span := uint32(2*int32(amp) + 1)
	for i, s := range buf {
		// xorshift32 — cheap, no allocation, good enough for dither.
		r := *rng
		r ^= r << 13
		r ^= r >> 17
		r ^= r << 5
		*rng = r
		d := int16(r%span) - amp
		v := int32(s) + int32(d)
		switch {
		case v > 32767:
			v = 32767
		case v < -32768:
			v = -32768
		}
		buf[i] = int16(v)
	}
}
