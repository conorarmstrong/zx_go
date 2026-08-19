package sam

import (
	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/saa1099"
)

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

// beeperFrame reconstructs one frame of the AC-coupled 1-bit speaker, one
// mono sample per audio frame.
//
// The AC-coupling and its clamp belong to the BEEPER, not to the mix. The
// clamp bounds a cone's excursion to the level it is driven to, because a
// high-pass's step response is the step height and a full toggle would
// otherwise overshoot to twice it. Running that over the summed bus made it a
// level cap on the SAA1099 as well, which reaches full scale on its own: it
// clipped a loud frame to a quarter of its range with nearly half the samples
// pinned on the limit. The SAA needs no DC blocking anyway — it is centred
// already, and a silent chip returns zeros.
func (m *Machine) beeperFrame(frames int) []int16 {
	if cap(m.beeperScratch) < frames {
		m.beeperScratch = make([]int16, frames)
	}
	out := m.beeperScratch[:frames]

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
		if level {
			out[i] = beeperAmplitude
		} else {
			out[i] = -beeperAmplitude
		}
	}

	// Anything left is an edge in the frame's OVERSHOOT window: ExecuteFrame
	// runs past its budget by the length of whichever instruction crossed the
	// boundary, so a write in that window carries an offset past
	// CyclesPerFrame and no sample can reach it. Fold those into the level
	// carried forward rather than dropping them — discarding one left the
	// speaker at the pre-write level for the whole of the next frame.
	for ; idx < len(m.beeperEvents); idx++ {
		level = m.beeperEvents[idx].high
	}
	m.frameStartBeeper = level
	m.beeperEvents = m.beeperEvents[:0]

	m.beeperDC.Process(out)
	return out
}

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
	// The list has to stay in T-state order: the frame builder is a single
	// forward scan that never looks back, so an event filed ahead of one
	// already recorded would be applied in the first sample window and its
	// successor's level would win for the rest of the frame. A write whose
	// timestamp appears to predate the frame — a restore that rewinds the CPU
	// clock without the frame start, say — is pinned to the last event instead
	// of to zero.
	if n := len(m.beeperEvents); n > 0 && off < m.beeperEvents[n-1].tstate {
		off = m.beeperEvents[n-1].tstate
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

	// The beeper is mono — one bit, one speaker — so it reaches both channels
	// equally, and it arrives already AC-coupled and clamped to the level the
	// cone is driven to.
	beeper := m.beeperFrame(frames)
	for i := 0; i < frames; i++ {
		contrib := int32(beeper[i])
		buf[i*2] = audio.SaturatingAdd16(buf[i*2], contrib)
		buf[i*2+1] = audio.SaturatingAdd16(buf[i*2+1], contrib)
	}
}

// DropAudioFrame discards the beeper events of the frame that just ended,
// carrying the speaker's level forward.
//
// It exists for the runs with no audio consumer — --no-sound, and every
// headless run — where GenerateAudioStereo is never called and the event list
// would otherwise grow for the lifetime of the process. The capture path makes
// that worse than a leak: Machine.SaveState gob-encodes the whole list on every
// rewind frame.
func (m *Machine) DropAudioFrame() {
	if len(m.beeperEvents) == 0 {
		return
	}
	// beeperLevel is the live bit, so it is where the frame actually ended.
	m.frameStartBeeper = m.beeperLevel
	m.beeperEvents = m.beeperEvents[:0]
}

// GenerateAudio is the same frame, kept under its original name for callers
// that already speak stereo.
func (m *Machine) GenerateAudio(buf []int16) { m.GenerateAudioStereo(buf) }
