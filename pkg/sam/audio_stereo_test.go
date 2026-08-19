package sam

import "testing"

// The SAM's audio is two sources with different shapes.
//
// The SAA1099 is a genuine stereo chip: six tone channels, each assignable to
// left, right or both, and pkg/saa1099 has always produced a real pair. What
// reached the speakers was the average of the two, because the audio device
// could only carry one channel.
//
// The beeper is the other half, and it was not modelled at all. The SAM keeps
// the Spectrum's 1-bit speaker on bit 4 of port $FE for compatibility, so
// software written for the 48K — and the SAM's own ROM, which clicks the keys —
// made no sound.

// beepAt writes the BEEP bit at a given T-state offset into the frame.
//
// It moves the CPU clock rather than using setFrameRel, which the other tests
// in this package use. ExecuteFrame rebases the T-state counter every frame, so
// after newTestMachine the clock reads 0 and setFrameRel's frameStart =
// Tstates - offset underflows to an enormous number. recordBeeper's guard then
// clamps every offset to 0 and the whole frame collapses onto one instant —
// which reads as a passing test for anything that only checks "something
// changed".
func beepAt(t *testing.T, m *Machine, offset uint64, high bool) {
	t.Helper()
	m.frameStart = 0
	m.CPU.SetTstates(offset)
	var v byte
	if high {
		v = borderBEEP
	}
	m.WritePort(0x00FE, v)
}

// writeSAA programmes one SAA1099 register through the machine's own port
// decode, so the test exercises the same path a guest does.
func writeSAA(m *Machine, reg, val byte) {
	m.WritePort(0x01FF, reg) // high byte 0x01: address latch
	m.WritePort(0x00FF, val) // data
}

// The stereo generator fills a pair per sample.
func TestTheStereoFrameIsInterleaved(t *testing.T) {
	m := newTestMachine(t)
	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)
	// Nothing to assert about the values on a silent chip; the contract under
	// test is that the whole buffer is filled rather than half of it.
	if len(buf) != SamplesPerFrame*2 {
		t.Fatalf("buffer length changed: %d", len(buf))
	}
}

// A SAA channel panned hard to one side must arrive on that side alone. The
// down-mix made this impossible to express: the two channels were averaged
// before anything downstream saw them.
func TestAHardPannedSAAChannelStaysOnItsSide(t *testing.T) {
	m := newTestMachine(t)
	// Channel 0: full amplitude on the left, nothing on the right, tone
	// enabled. Register numbers are from the SAA1099 datasheet: $00-$05 are
	// the per-channel amplitudes (low nibble left, high nibble right), $08-$0D
	// the frequencies, $10-$11 the octaves, $14 the tone enables, $1C the
	// reset/enable byte.
	writeSAA(m, 0x1C, 0x02) // clear reset
	writeSAA(m, 0x00, 0x0F) // channel 0: left 15, right 0
	writeSAA(m, 0x08, 0x80) // channel 0 frequency
	writeSAA(m, 0x10, 0x33) // octaves for channels 0 and 1
	writeSAA(m, 0x14, 0x01) // enable tone on channel 0
	writeSAA(m, 0x1C, 0x01) // sound enable

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)

	var maxL, maxR int16
	for i := 0; i+1 < len(buf); i += 2 {
		if buf[i] > maxL {
			maxL = buf[i]
		}
		if buf[i+1] > maxR {
			maxR = buf[i+1]
		}
	}
	if maxL == 0 {
		t.Fatal("a left-panned channel produced nothing on the left")
	}
	if maxR != 0 {
		t.Errorf("a left-panned channel reached the right channel (peak %d)", maxR)
	}
}

// The beeper makes sound. Holding the bit high for a whole frame is a DC level
// rather than a tone, so what is asserted is that the samples moved at all.
func TestTheBeeperReachesTheOutput(t *testing.T) {
	m := newTestMachine(t)

	silent := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(silent)

	beepAt(t, m, 0, true)
	loud := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(loud)

	if silent[0] == loud[0] {
		t.Fatal("setting the BEEP bit changed nothing: the beeper is not in the mix")
	}
}

// The beeper is one bit driving one speaker, so it arrives equally on both
// channels rather than acquiring an image from the plumbing.
func TestTheBeeperIsCentred(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, true)

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)
	for i := 0; i+1 < len(buf); i += 2 {
		if buf[i] != buf[i+1] {
			t.Fatalf("frame %d = (%d, %d): the beeper is not centred", i/2, buf[i], buf[i+1])
		}
	}
}

// A mid-frame toggle has to land at the T-state it happened, not at a frame
// boundary. Without that the beeper reproduces pitch but not timbre, and
// beeper-engine music comes out as noise.
func TestABeeperToggleLandsAtItsTState(t *testing.T) {
	m := newTestMachine(t)
	// Low for the first half of the frame, high for the second.
	beepAt(t, m, 0, false)
	beepAt(t, m, CyclesPerFrame/2, true)

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)

	early := buf[0]
	late := buf[(SamplesPerFrame-1)*2]
	if early == late {
		t.Fatalf("first sample %d and last %d are equal: the toggle was not placed in time",
			early, late)
	}
}

// The event list must not survive the frame it belongs to. Carrying it would
// replay the same toggles every frame, which is a continuous tone from a
// single click.
func TestTheBeeperEventsAreConsumedByTheFrame(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, false)
	beepAt(t, m, CyclesPerFrame/2, true)

	first := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(first)

	second := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(second)

	// An edge is a large single-sample jump, and there was exactly one of them.
	// The second frame holds the level the toggle left, so after AC-coupling it
	// decays smoothly toward silence: no jump anywhere near the size of a real
	// edge. Comparing jumps rather than levels is what makes this independent
	// of the DC blocker's time constant.
	firstJump := largestJump(first)
	secondJump := largestJump(second)
	if firstJump < int32(beeperAmplitude) {
		t.Fatalf("the first frame's largest jump was %d, want at least %d: no edge in it",
			firstJump, beeperAmplitude)
	}
	if secondJump > int32(beeperAmplitude)/8 {
		t.Errorf("the second frame's largest jump was %d, want a smooth decay: the first "+
			"frame's toggles are being replayed", secondJump)
	}
}

// An isolated toggle must click at the speaker's level, not at twice it.
//
// A high-pass filter's step response is the step height, and a full low-to-high
// toggle is a 2·amplitude step, so the AC-coupled output overshoots to double
// the level the speaker is actually driven to. A cone cannot deflect past its
// drive level, so the output is bounded to it — the same clamp pkg/ula applies
// to the Spectrum's beeper, and it was missing here: the SAM's filter was
// constructed with no limit at all, which falls back to full scale.
func TestAnIsolatedToggleIsClampedToTheSpeakerLevel(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, false)
	beepAt(t, m, CyclesPerFrame/2, true)

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)

	var peak int16
	for _, v := range buf {
		if v > peak {
			peak = v
		}
	}
	if peak > beeperAmplitude {
		t.Errorf("peak = %d, want no more than the speaker level %d: the high-pass "+
			"step response is doubling an isolated toggle", peak, beeperAmplitude)
	}
	if peak == 0 {
		t.Error("the toggle produced nothing")
	}
}

// largestJump returns the biggest absolute difference between consecutive
// samples on the left channel.
func largestJump(frame []int16) int32 {
	var max int32
	for i := 2; i+1 < len(frame); i += 2 {
		d := int32(frame[i]) - int32(frame[i-2])
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

// Writing the same BEEP level twice is not an edge and must not be recorded as
// one. The border port carries the colour, MIC and SOFF as well, so a program
// changing the border colour writes this port constantly.
func TestABorderWriteThatLeavesBEEPAloneIsNotAnEdge(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, true)
	// Several border colour changes, BEEP held high throughout.
	for i := uint64(1); i < 6; i++ {
		m.CPU.SetTstates(i * 1000)
		m.WritePort(0x00FE, borderBEEP|byte(i))
	}

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)
	for i := 2; i+1 < len(buf); i += 2 {
		if buf[i] != buf[0] {
			t.Fatalf("sample %d = %d, want the constant %d: a border colour change "+
				"was treated as a speaker edge", i/2, buf[i], buf[0])
		}
	}
}
