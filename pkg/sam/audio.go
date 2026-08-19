package sam

import "github.com/conorarmstrong/zx_go/pkg/saa1099"

// SamplesPerFrame is the number of stereo sample pairs one 50 Hz SAM frame
// produces at the audio sample rate.
const SamplesPerFrame = saa1099.SampleRate / 50

// The SAM has two sound sources, and they have different shapes.
//
// The SAA1099 is a stereo chip: each of its six tone channels carries a left
// and a right amplitude in the two nibbles of its own register, so a channel
// can be panned anywhere between the speakers. That is the SAM's music and
// effects hardware and the reason the machine has a stereo output at all.
//
// The beeper is the other one, and it is the Spectrum's: one bit on port $FE
// driving one speaker. Software written for the 48K uses it, and so does the
// SAM's own ROM for its key click. Being one bit and one speaker, it is mono,
// and it reaches both channels equally.

// beeperAmplitude is the level the 1-bit speaker drives to. It is well below
// full scale because the beeper is summed with the SAA rather than replacing
// it, and because the DC blocker's step response is the step height itself —
// an isolated toggle would otherwise clip.
const beeperAmplitude int16 = 8000

// beeperEvent is one speaker transition, timestamped within the frame.
type beeperEvent struct {
	tstate int
	high   bool
}

// recordBeeper notes a BEEP-bit transition at the T-state it happened.
//
// Only a CHANGE is recorded. Port $FE also carries the border colour, the MIC
// bit and SOFF, so a program animating the border writes it constantly; taking
// every write as an edge would turn a border effect into a buzz.
func (m *Machine) recordBeeper(val byte) {
	high := val&borderBEEP != 0
	if high == m.beeperLevel {
		return
	}
	m.beeperLevel = high
	now := m.CPU.Tstates()
	var off int
	if now > m.frameStart {
		off = int(now - m.frameStart)
	}
	m.beeperEvents = append(m.beeperEvents, beeperEvent{tstate: off, high: high})
}

// GenerateAudioStereo fills buf with one frame of interleaved left/right 16-bit
// audio: the SAA1099's own stereo pair, with the beeper summed equally into
// both channels. buf must hold 2·SamplesPerFrame values.
//
// It consumes the frame's recorded beeper events and carries the final speaker
// level into the next frame, so a level held across a boundary stays held
// rather than restarting.
func (m *Machine) GenerateAudioStereo(buf []int16) {
	frames := len(buf) / 2
	if frames == 0 {
		return
	}
	m.SAA.GenerateStereo(buf[:frames*2])

	level := m.frameStartBeeper
	idx := 0
	for i := 0; i < frames; i++ {
		sampleEnd := (i + 1) * CyclesPerFrame / frames
		// Take the level in force at the END of the sample window. The SAM's
		// beeper is a square wave rather than a DAC, so sampling it is the
		// right model; box-filtering would round the edges of a signal whose
		// edges are the whole content.
		for idx < len(m.beeperEvents) && m.beeperEvents[idx].tstate < sampleEnd {
			level = m.beeperEvents[idx].high
			idx++
		}
		var contrib int32
		if level {
			contrib = int32(beeperAmplitude)
		} else {
			contrib = -int32(beeperAmplitude)
		}
		buf[i*2] = clampInt16(int32(buf[i*2]) + contrib)
		buf[i*2+1] = clampInt16(int32(buf[i*2+1]) + contrib)
	}

	m.frameStartBeeper = level
	m.beeperEvents = m.beeperEvents[:0]

	// AC-couple each channel, as the machine's output capacitor does. Without
	// it a speaker held at either level is a DC rail rather than silence: the
	// beeper rests LOW, so an idle SAM would sit at -beeperAmplitude for ever
	// and the first toggle would step from there.
	m.beeperDC.ProcessStereo(buf[:frames*2])
}

// GenerateAudio is the same frame, kept under its original name for callers
// that already speak stereo.
func (m *Machine) GenerateAudio(buf []int16) { m.GenerateAudioStereo(buf) }

func clampInt16(v int32) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}
