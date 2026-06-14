// Package dac implements the Spectrum Next's four 8-bit DACs.
// Each channel is a single byte register whose value is the
// current speaker level; the mixer samples this at the audio
// sample rate and adds it to the AY / beeper / DAC sum.
//
// Port mapping (per the SpecNext wiki):
//
//	Channel A: 0x0F or 0xF1   (FairLight / Profi)
//	Channel B: 0x1F or 0xF3
//	Channel C: 0xF9 or 0xDF   (Specdrum-compatible)
//	Channel D: 0xFB
//
// Each channel has two alias ports — both are decoded; writing
// to either updates that channel. The DAC ports are decoded on
// the low byte only, matching real hardware.
package dac

// Channel identifies one of the four DACs.
type Channel int

const (
	ChannelA Channel = 0
	ChannelB Channel = 1
	ChannelC Channel = 2
	ChannelD Channel = 3
)

// Bank is the four-channel DAC bank.
type Bank struct {
	levels [4]byte
}

// New returns a Bank with all channels at level 0 (silent).
func New() *Bank { return &Bank{} }

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
	switch port & 0xFF {
	case 0x0F, 0xF1:
		b.levels[ChannelA] = val
	case 0x1F, 0xF3:
		b.levels[ChannelB] = val
	case 0xF9, 0xDF:
		b.levels[ChannelC] = val
	case 0xFB:
		b.levels[ChannelD] = val
	default:
		return false
	}
	return true
}

// Reset clears all four DAC levels to 0.
func (b *Bank) Reset() {
	for i := range b.levels {
		b.levels[i] = 0
	}
}

// MixedLevel returns the mean of the four channel levels as an
// 8-bit unsigned value. The mixer in pkg/ula uses this to fold
// DAC output into the global audio sum. Channels at 0 contribute
// silence; the divide-by-four prevents the sum from saturating
// when all four channels are at max.
func (b *Bank) MixedLevel() byte {
	sum := uint16(b.levels[0]) + uint16(b.levels[1]) + uint16(b.levels[2]) + uint16(b.levels[3])
	return byte(sum / 4)
}

// dacMixAmplitude scales the centred 8-bit DAC value (range -128..127
// after subtracting 128) up to a usable int16 amplitude. 64 puts the
// peak-to-peak DAC range at ±8128 — enough to be clearly audible
// alongside the beeper (peaks at ±20000) and the AY (similar
// magnitude) without saturating their combined sum at int16 limits.
const dacMixAmplitude int16 = 64

// MixInto adds the current DAC output to every sample in buf. v1.0
// uses the level snapshot at the moment Read() runs — chiptune
// sample-playback that writes the DAC at audio-rate within a frame
// will be heard but with reduced fidelity (the rapid writes flatten
// to the average over the read window). A v1.1 upgrade can record
// per-write events with T-state timestamps and integrate as the
// beeper does.
//
// The contribution is centred: a DAC value of 0x80 produces zero
// offset; 0x00 and 0xFF produce maximal negative and positive
// contributions. This matches the convention real DACs use when
// driving a speaker (output should sit at 0V mean to avoid
// loudspeaker offset).
func (b *Bank) MixInto(buf []int16) {
	level := int16(b.MixedLevel()) - 128
	contrib := int32(level) * int32(dacMixAmplitude)
	if contrib == 0 {
		return
	}
	for i := range buf {
		// Saturating add: the sum of beeper (±20000) + AY (similar)
		// + DAC (±8128) can exceed int16 range. Without saturation
		// the wrap-around produces audible pops at extrema.
		sum := int32(buf[i]) + contrib
		switch {
		case sum > 32767:
			buf[i] = 32767
		case sum < -32768:
			buf[i] = -32768
		default:
			buf[i] = int16(sum)
		}
	}
}
