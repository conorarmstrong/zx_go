package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/next/dac"
)

// The frame the ULA hands to the audio system is interleaved stereo.
//
// Almost everything it folds in is mono by construction: the beeper is one bit
// driving one speaker, the tape's EAR line is one bit, and SpecDrum and Covox
// are single-ended 8-bit DACs on the edge connector. Those go to both channels.
// The Next's DAC bank is the source that is genuinely two-sided, and the ULA is
// where its pair has to survive.

// leftRight splits an interleaved frame into its two channels.
func leftRight(frame []int16) (l, r []int16) {
	for i := 0; i+1 < len(frame); i += 2 {
		l = append(l, frame[i])
		r = append(r, frame[i+1])
	}
	return l, r
}

// allSame reports whether every element equals v.
func allSame(xs []int16, v int16) bool {
	for _, x := range xs {
		if x != v {
			return false
		}
	}
	return true
}

func newStereoTestULA(t *testing.T) *ULA {
	t.Helper()
	u := &ULA{}
	u.audio = &audio.AudioSystem{}
	return u
}

// The mixed frame is one stereo frame per audio sample, not one value.
func TestTheMixedFrameIsInterleavedStereo(t *testing.T) {
	u := newStereoTestULA(t)
	frame := u.mixAudioFrame()
	if len(frame) != audio.SamplesPerFrame*2 {
		t.Fatalf("frame = %d values, want %d (%d samples interleaved)",
			len(frame), audio.SamplesPerFrame*2, audio.SamplesPerFrame)
	}
}

// With nothing but the beeper, both channels carry the same signal. A machine
// with no stereo source must not acquire an image from the plumbing.
func TestABeeperOnlyFrameIsIdenticalOnBothChannels(t *testing.T) {
	u := newStereoTestULA(t)
	u.frameStartSpeakerState = true

	l, r := leftRight(u.mixAudioFrame())
	for i := range l {
		if l[i] != r[i] {
			t.Fatalf("sample %d = (%d, %d): a mono source reached the two channels differently",
				i, l[i], r[i])
		}
	}
}

// The Next DAC's stereo image has to survive into the frame. This is the whole
// point of the change, and it is the case the old mono fold destroyed: a hard
// left/right pair averaged to silence.
func TestTheNextDACPairReachesTheFrame(t *testing.T) {
	u := newStereoTestULA(t)
	b := dac.New()
	u.SetNextDAC(b)

	b.WritePort(0x1F, 0xFF) // channel A: left, full scale
	b.WritePort(0x0F, 0xFF) // channel B: left, full scale
	b.Record(0)

	l, r := leftRight(u.mixAudioFrame())
	if allSame(l, l[0]) && allSame(r, r[0]) && l[0] == r[0] {
		t.Fatal("both channels carry the same value: the DAC pair was folded to mono")
	}
	// The right pair was never written, so it stays at its rest level and
	// contributes nothing beyond the beeper both channels already share.
	if !allSame(r, r[0]) {
		t.Errorf("the right channel moved with no write to channel C or D")
	}
}

// A silently-disconnected DAC is the failure this test exists for. The frame
// path used to reach the bank through a runtime type assertion, so renaming the
// method it looked for detached the DAC with no build error and no test
// failure — the assertion simply stopped matching. The interface now declares
// it, and this checks the wiring end to end rather than trusting that.
func TestTheNextDACIsActuallyConsulted(t *testing.T) {
	u := newStereoTestULA(t)
	quiet := u.mixAudioFrame()

	b := dac.New()
	u.SetNextDAC(b)
	b.WritePort(0x4F, 0x00) // channel C hard to the negative rail
	b.WritePort(0xFB, 0x00) // channel D likewise
	b.Record(0)

	loud := u.mixAudioFrame()
	if quiet[1] == loud[1] {
		t.Fatal("driving the DAC changed nothing: the bank is not in the frame path")
	}
}

// Each channel gets its own DC-blocking filter. One filter walking an
// interleaved buffer would alternate between the channels, so every sample
// would be high-passed against the other channel's previous sample: a hard
// left/right pair would leak into both filters and neither would settle.
func TestTheDCBlockerRunsPerChannel(t *testing.T) {
	u := newStereoTestULA(t)
	u.dcEnabled = true
	u.dc.SetLimit(int32(beeperHigh))

	b := dac.New()
	u.SetNextDAC(b)
	// A steady hard-left signal: left held up, right left at rest.
	b.WritePort(0x1F, 0xFF)
	b.WritePort(0x0F, 0xFF)
	b.Record(0)

	// Run several frames so the filters settle.
	var l, r []int16
	for i := 0; i < 4; i++ {
		b.Record(0)
		l, r = leftRight(u.mixAudioFrame())
	}

	// The right channel saw a constant level throughout, so its filter must
	// have settled to silence. If one filter served both, the left channel's
	// steps would still be showing up here.
	for i, v := range r {
		if v < -2 || v > 2 {
			t.Fatalf("right channel sample %d = %d, want ~0: it is being filtered "+
				"against the left channel's history", i, v)
		}
	}
	if len(l) == 0 {
		t.Fatal("no samples produced")
	}
}
