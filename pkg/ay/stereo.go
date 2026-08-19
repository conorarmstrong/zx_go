package ay

// AY stereo panning.
//
// The three channels of an AY are separate outputs on the die; what reaches a
// speaker depends entirely on how the machine wires them. A 128K, +2, +2A or
// +3 sums all three into one internal speaker, so those machines are mono and
// stay that way. The Spectrum Next routes them through turbosound.vhd, which
// pans them per NR$08 bit 5 and can force any individual chip back to mono via
// NR$09 bits 7:5.
//
// The panning law is turbosound.vhd:186-192, resolved:
//
//	mono: L = R = A + B + C
//	ABC:  L = A + B,  R = B + C     (B is the centre channel)
//	ACB:  L = A + C,  R = B + C     (C is the centre channel)
//
// Only the middle letter moves between the two stereo cases, which is the
// whole content of the naming: ABC and ACB differ in nothing except which
// channel is heard from both speakers.

// StereoMode selects how a chip's three channels are panned.
type StereoMode uint8

const (
	// StereoMono sums all three channels into both outputs. The default, and
	// the only correct setting for every classic machine with an AY fitted.
	StereoMono StereoMode = iota
	// StereoABC pans A left, B centre, C right (NR$08 bit 5 clear).
	StereoABC
	// StereoACB pans A left, C centre, B right (NR$08 bit 5 set).
	StereoACB
)

// String makes a failed comparison in a test name the mode rather than a
// number.
func (m StereoMode) String() string {
	switch m {
	case StereoABC:
		return "ABC"
	case StereoACB:
		return "ACB"
	default:
		return "mono"
	}
}

// mixHeadroom divides the summed channel levels on the way into the int16 mix.
//
// The FPGA has no equivalent: it widens the bus instead, summing three 8-bit
// channels into the 12-bit ay_L_i / ay_R_i that audio_mixer.vhd expects. We
// mix into a fixed int16 shared with the beeper, so the sum is scaled to fit
// alongside it. Three is the channel count, and applying the same divisor to
// every mode keeps the panned output at exactly the loudness the mono path
// has always produced.
const mixHeadroom = 3

// SetStereoMode selects the panning law. Safe to call from the emulation
// goroutine while the audio goroutine is mixing.
func (a *AY) SetStereoMode(m StereoMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stereo = m
}

// StereoModeSetting reports the current panning law.
func (a *AY) StereoModeSetting() StereoMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stereo
}

// pannedLevels returns the current channel levels resolved through the panning
// law, unclamped and before the headroom divide. Must be called with the mutex
// held.
func (a *AY) pannedLevels() (int32, int32) {
	chA := int32(a.channelLevel(0))
	chB := int32(a.channelLevel(1))
	chC := int32(a.channelLevel(2))
	switch a.stereo {
	case StereoABC:
		return chA + chB, chB + chC
	case StereoACB:
		return chA + chC, chB + chC
	default:
		m := chA + chB + chC
		return m, m
	}
}

// MixIntoStereo generates one sample per stereo frame in buf and adds it in,
// so the AY sums with whatever the beeper and DAC have already contributed —
// the same arrangement as audio_mixer.vhd, which adds ay_L_i / ay_R_i to the
// other sources rather than replacing them.
//
// buf is interleaved (L, R, L, R ...). A trailing odd slot is left alone: it
// is not half a frame, and advancing the chip's clock for it would put the
// generators out of step with the frames that follow.
func (a *AY) MixIntoStereo(buf []int16) {
	if len(buf) < 2 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	cps := cyclesPerSample()
	for i := 0; i+1 < len(buf); i += 2 {
		a.advanceClock(cps)
		l, r := a.pannedLevels()
		buf[i] = clampSample(int32(buf[i]) + l/mixHeadroom)
		buf[i+1] = clampSample(int32(buf[i+1]) + r/mixHeadroom)
	}
}
