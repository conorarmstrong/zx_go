package sam

import "github.com/conorarmstrong/zx_go/pkg/saa1099"

// SamplesPerFrame is the number of stereo sample pairs one 50 Hz SAM frame
// produces at the audio sample rate.
const SamplesPerFrame = saa1099.SampleRate / 50

// GenerateAudio fills buf with one frame of interleaved left/right 16-bit audio
// from the SAA1099 (buf length should be 2·SamplesPerFrame). The GUI calls this
// once per frame and feeds it to a stereo sink.
//
// The 1-bit beeper (BORDER bit 4) is mixed in by the audio-system integration
// (it needs the event-timed waveform reconstruction the ULA uses); the SAA is
// the SAM's music/SFX chip and is the audio Sprint 5 delivers.
func (m *Machine) GenerateAudio(buf []int16) {
	m.SAA.GenerateStereo(buf)
}
