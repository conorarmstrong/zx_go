// Package dac implements the Spectrum Next's four 8-bit DACs.
// Each channel is a single byte register whose value is the
// current speaker level; the mixer samples this at the audio
// sample rate and adds it to the AY / beeper / DAC sum.
//
// Port mapping (soundrive-1 / soundrive-2 columns of the ports.txt DAC
// channel table):
//
//	Channel A: 0x1F or 0xF1
//	Channel B: 0x0F or 0xF3
//	Channel C: 0x4F or 0xF9
//	Channel D: 0xFB
//
// Each channel has two alias ports — both are decoded; writing
// to either updates that channel. The DAC ports are decoded on
// the low byte only, matching real hardware.
package dac

import "github.com/conorarmstrong/zx_go/pkg/audio"

// Channel identifies one of the four DACs.
type Channel int

const (
	ChannelA Channel = 0
	ChannelB Channel = 1
	ChannelC Channel = 2
	ChannelD Channel = 3
)

// restLevel is the value the four channels power up and reset to.
//
// soundrive.vhd:71-74 loads X"80" into all four on reset, and mid-scale is the
// only value that means silence for a DAC whose output is centred on $80. A
// bank resetting to 0 sits on the negative rail instead: a full-scale DC offset
// that the AC-coupling further down then removes, so it is inaudible AND wrong,
// which is the combination that keeps a bug alive.
const restLevel byte = 0x80

// Bank is the four-channel DAC bank.
//
// The channels are two stereo pairs, not four interchangeable voices:
// soundrive.vhd sums chA+chB into pcm_L_o and chC+chD into pcm_R_o (:110-111),
// and labels them "-- left" and "-- right" in its port list.
type Bank struct {
	levels [4]byte
	// Event-timed reconstruction: each port write records the resulting pair of
	// levels with its T-state offset within the frame, so the audio frame can be
	// reconstructed sample-accurately (box-filter) instead of snapshotting one
	// level per audio pull. Carries the last pair across frames.
	//
	// Both sides are recorded per event rather than one mixed level, because
	// the two sides move independently: a program writing a stereo sample
	// touches the left pair and the right pair at different T-states, and a
	// single timeline could not represent that.
	events []dacEvent
	startL byte
	startR byte
}

type dacEvent struct {
	tstateOffset int
	left         byte
	right        byte
}

// New returns a Bank at its power-on rest level, which is silence.
func New() *Bank {
	b := &Bank{}
	b.Reset()
	return b
}

// Record appends a timed event capturing the current output pair at the given
// T-state offset within the frame. The ULA calls this after each DAC port write
// so GenerateFrameStereo can reconstruct the waveform sample-accurately.
func (b *Bank) Record(tstateOffset int) {
	b.events = append(b.events, dacEvent{
		tstateOffset: tstateOffset,
		left:         b.LevelL(),
		right:        b.LevelR(),
	})
}

// GenerateFrameStereo reconstructs one frame of interleaved stereo DAC samples
// from the recorded writes (box-filter integration, like the beeper), then
// clears the events and carries the final pair into the next frame. The
// level→amplitude mapping matches MixIntoStereo so the timed path is the same
// loudness as the per-pull snapshot, only sample-accurate.
func (b *Bank) GenerateFrameStereo(samplesPerFrame, tstatesPerFrame int) []int16 {
	out := make([]int16, samplesPerFrame*2)
	left, right := b.startL, b.startR
	idx := 0
	for i := 0; i < samplesPerFrame; i++ {
		sampleStart := i * tstatesPerFrame / samplesPerFrame
		sampleEnd := (i + 1) * tstatesPerFrame / samplesPerFrame
		sampleLen := sampleEnd - sampleStart
		var accL, accR int64
		cur := sampleStart
		for idx < len(b.events) && b.events[idx].tstateOffset < sampleEnd {
			next := b.events[idx].tstateOffset
			if next < cur {
				next = cur
			}
			accL += int64(left) * int64(next-cur)
			accR += int64(right) * int64(next-cur)
			left, right = b.events[idx].left, b.events[idx].right
			cur = next
			idx++
		}
		accL += int64(left) * int64(sampleEnd-cur)
		accR += int64(right) * int64(sampleEnd-cur)
		avgL, avgR := left, right
		if sampleLen > 0 {
			avgL = byte(accL / int64(sampleLen))
			avgR = byte(accR / int64(sampleLen))
		}
		out[i*2] = centredAmplitude(avgL)
		out[i*2+1] = centredAmplitude(avgR)
	}
	b.startL, b.startR = left, right
	b.events = b.events[:0]
	return out
}

// centredAmplitude maps an 8-bit DAC level to a signed amplitude about the
// $80 rest level, so silence is 0 rather than a DC rail.
func centredAmplitude(level byte) int16 {
	return (int16(level) - int16(restLevel)) * dacMixAmplitude
}

// Level returns the current 8-bit level for the given channel.
// Channels outside ChannelA..ChannelD return 0.
func (b *Bank) Level(c Channel) byte {
	if c < ChannelA || c > ChannelD {
		return 0
	}
	return b.levels[c]
}

// WritePort accepts a port write and updates the appropriate
// channel's level if the port matches one of the documented DAC
// ports. Returns true if the port was a DAC port (and was
// handled), false otherwise — the caller (ULA's port dispatcher)
// uses this as a fall-through signal.
func (b *Bank) WritePort(port uint16, val byte) bool {
	// Port to channel per the FPGA's port_dac_A..D decode
	// (zxnext.vhd:2658-2664), taking the union of the DAC modes: the
	// NR$82-85 internal_port_enable bits that gate them on hardware are
	// not plumbed here, and this decode already took the union for the
	// ports it knew about. $5F (channel D in SounDrive mode 1 and in
	// stereo A/D) and $DF (the SpecDrum mono A+D pair) were missing, so
	// the right-hand pair stayed at mid-scale and a hard-panned right
	// DAC was silent. See SounDrive for the mode-gated reference; $3F
	// and $B3 stay out of this map because both alias decodes the ULA
	// dispatcher already owns.
	switch port & 0xFF {
	case 0x1F, 0xF1:
		b.levels[ChannelA] = val
	case 0x0F, 0xF3:
		b.levels[ChannelB] = val
	case 0x4F, 0xF9:
		b.levels[ChannelC] = val
	case 0x5F, 0xFB:
		b.levels[ChannelD] = val
	case 0xDF: // mono_AD
		b.levels[ChannelA] = val
		b.levels[ChannelD] = val
	default:
		return false
	}
	return true
}

// Reset returns all four DAC levels to mid-scale and clears the event-timing
// state, matching soundrive.vhd's reset branch.
func (b *Bank) Reset() {
	for i := range b.levels {
		b.levels[i] = restLevel
	}
	b.events = b.events[:0]
	b.startL, b.startR = restLevel, restLevel
}

// LevelL returns the left output as an 8-bit level: the mean of channels A and
// B, which soundrive.vhd sums into pcm_L_o.
//
// The FPGA widens instead of averaging (its pcm_L_o is 9 bits for a 0-510
// range) because it has a wider bus downstream. We mix into a fixed int16
// shared with the beeper and the AY, so the pair is meaned to stay in range —
// the same headroom choice the four-channel mean made before, and it keeps a
// guest driving all four channels together at exactly the level it had.
func (b *Bank) LevelL() byte {
	return byte((uint16(b.levels[ChannelA]) + uint16(b.levels[ChannelB])) / 2)
}

// LevelR returns the right output: the mean of channels C and D, which
// soundrive.vhd sums into pcm_R_o.
func (b *Bank) LevelR() byte {
	return byte((uint16(b.levels[ChannelC]) + uint16(b.levels[ChannelD])) / 2)
}

// dacMixAmplitude scales the centred 8-bit DAC value (range -128..127
// after subtracting 128) up to a usable int16 amplitude. 64 puts the
// peak-to-peak DAC range at ±8128 — enough to be clearly audible
// alongside the beeper (peaks at ±20000) and the AY (similar
// magnitude) without saturating their combined sum at int16 limits.
const dacMixAmplitude int16 = 64

// MixIntoStereo adds the current DAC output pair to every stereo frame in buf
// as a flat per-call snapshot. The ULA does not use this for the Next DAC — it
// drives the event-timed GenerateFrameStereo (sample-accurate, like the
// beeper). This satisfies the generic audio.DACSource interface.
//
// The contribution is centred: a DAC value of 0x80 produces zero
// offset; 0x00 and 0xFF produce maximal negative and positive
// contributions. This matches the convention real DACs use when
// driving a speaker (output should sit at 0V mean to avoid
// loudspeaker offset).
func (b *Bank) MixIntoStereo(buf []int16) {
	contribL := int32(centredAmplitude(b.LevelL()))
	contribR := int32(centredAmplitude(b.LevelR()))
	if contribL == 0 && contribR == 0 {
		return
	}
	for i := 0; i+1 < len(buf); i += 2 {
		buf[i] = audio.SaturatingAdd16(buf[i], contribL)
		buf[i+1] = audio.SaturatingAdd16(buf[i+1], contribR)
	}
}
