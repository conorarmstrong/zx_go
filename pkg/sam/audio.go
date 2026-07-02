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
// the SAM's dedicated music/SFX chip.
func (m *Machine) GenerateAudio(buf []int16) {
	m.SAA.GenerateStereo(buf)
}

// GenerateAudioMono fills buf (length SamplesPerFrame) with one frame of the
// SAA1099 output downmixed to mono — the average of the left and right channels.
// The GUI uses this to feed the shared (currently mono) audio device; true
// stereo output awaits the shared audio path being widened. A scratch buffer is
// reused across calls to avoid per-frame allocation.
func (m *Machine) GenerateAudioMono(buf []int16) {
	if cap(m.audioScratch) < 2*len(buf) {
		m.audioScratch = make([]int16, 2*len(buf))
	}
	stereo := m.audioScratch[:2*len(buf)]
	m.SAA.GenerateStereo(stereo)
	for i := range buf {
		buf[i] = int16((int32(stereo[2*i]) + int32(stereo[2*i+1])) / 2)
	}
}
