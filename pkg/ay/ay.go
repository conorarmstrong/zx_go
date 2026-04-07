// Package ay implements the AY-3-8912 sound chip used in the ZX Spectrum
// 128K, +2, +2A and +3 models.
//
// The AY-3-8912 has three independent square-wave tone channels, a single
// pseudo-random noise generator and a hardware envelope generator. The chip
// is accessed via two I/O ports:
//
//	0xFFFD - register select (write) / register read (read)
//	0xBFFD - register data write
//
// There are 16 registers (R0-R15). The chip is clocked at 1.7734MHz which is
// half of the ZX Spectrum's 3.5MHz CPU clock. Internally the AY further
// divides this clock by 16 for the tone/noise generators and by 256 for the
// envelope generator.
package ay

import (
	"sync"
)

// Number of AY registers.
const (
	NumRegisters = 16

	// AYClock is the AY-3-8912 master clock in Hz on the ZX Spectrum
	// (3.5MHz CPU / 2).
	AYClock = 1773400

	// SampleRate is the audio output sample rate.
	SampleRate = 44100
)

// Register indices for clarity.
const (
	RegToneALow  = 0
	RegToneAHigh = 1
	RegToneBLow  = 2
	RegToneBHigh = 3
	RegToneCLow  = 4
	RegToneCHigh = 5
	RegNoise     = 6
	RegMixer     = 7
	RegVolumeA   = 8
	RegVolumeB   = 9
	RegVolumeC   = 10
	RegEnvLow    = 11
	RegEnvHigh   = 12
	RegEnvShape  = 13
	RegIOA       = 14
	RegIOB       = 15
)

// Logarithmic 4-bit volume table for the AY-3-8912 (output level for each
// volume value 0..15). These values are linearised into 16-bit signed
// amplitudes — they roughly follow a 3 dB-per-step curve which approximates
// the real chip's output. Levels are scaled so that the loudest channel by
// itself never quite reaches int16 saturation, leaving headroom for mixing
// with the other two channels and the beeper.
var volumeTable = [16]int16{
	0, 90, 130, 180,
	260, 370, 520, 730,
	1030, 1460, 2060, 2920,
	4120, 5830, 8250, 11650,
}

// AY represents an AY-3-8912 sound chip instance.
type AY struct {
	mu sync.Mutex

	// 16 hardware registers
	regs [NumRegisters]byte

	// Currently selected register (set via port 0xFFFD writes)
	selected byte

	// Tone channel state — 16-bit counter and current square-wave output bit
	toneCounter [3]int
	toneOutput  [3]byte // 0 or 1

	// Noise generator state
	noiseCounter int
	noiseLFSR    uint32 // 17-bit LFSR for the noise output
	noiseOutput  byte   // 0 or 1

	// Envelope generator
	envCounter  int
	envStep     int  // 0..31
	envHolding  bool // envelope finished and holding
	envAttack   bool // current direction (true = rising)
	envContinue bool
	envAlt      bool
	envHold     bool

	// Sub-counters for clock division. The AY is clocked at AYClock Hz; this
	// counter accumulates fractional cycles per audio sample so we can advance
	// the internal state by an exact integer number of master cycles.
	clockAccum float64
}

// New creates a new AY instance with all registers cleared and the noise
// LFSR seeded.
func New() *AY {
	a := &AY{
		noiseLFSR: 1,
	}
	a.Reset()
	return a
}

// Reset clears all registers and internal state.
func (a *AY) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.regs {
		a.regs[i] = 0
	}
	a.selected = 0
	for i := 0; i < 3; i++ {
		a.toneCounter[i] = 0
		a.toneOutput[i] = 0
	}
	a.noiseCounter = 0
	a.noiseLFSR = 1
	a.noiseOutput = 0
	a.envCounter = 0
	a.envStep = 0
	a.envHolding = false
	a.envAttack = false
	a.envContinue = false
	a.envAlt = false
	a.envHold = false
	a.clockAccum = 0

	// Default mixer: all channels muted (bits 0-5 high = disabled).
	a.regs[RegMixer] = 0x3F
}

// SelectRegister latches the register that will be read or written via the
// data port. Only the low 4 bits are significant.
func (a *AY) SelectRegister(reg byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.selected = reg & 0x0F
}

// WriteSelected writes a value into whichever register has been selected via
// SelectRegister.
func (a *AY) WriteSelected(val byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writeRegisterLocked(a.selected, val)
}

// WriteRegister writes a value to a specific register, bypassing the
// register-select latch. Mostly useful for tests and snapshot loading.
func (a *AY) WriteRegister(reg, val byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writeRegisterLocked(reg&0x0F, val)
}

// ReadRegister returns the current value of a register.
func (a *AY) ReadRegister(reg byte) byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.regs[reg&0x0F]
}

// ReadSelected returns the value of the currently selected register, used by
// reads of port 0xFFFD.
func (a *AY) ReadSelected() byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.regs[a.selected]
}

// writeRegisterLocked must be called with the mutex held.
func (a *AY) writeRegisterLocked(reg, val byte) {
	// Mask values that have less than 8 valid bits — this matches the real
	// chip and avoids surprising behaviour when programs read the registers
	// back.
	switch reg {
	case RegToneAHigh, RegToneBHigh, RegToneCHigh:
		val &= 0x0F
	case RegNoise:
		val &= 0x1F
	case RegMixer:
		// Mixer is 6 bits but bits 6/7 control I/O port direction; preserve
		// them as written.
	case RegVolumeA, RegVolumeB, RegVolumeC:
		val &= 0x1F
	case RegEnvShape:
		val &= 0x0F
	}

	a.regs[reg] = val

	if reg == RegEnvShape {
		a.startEnvelope(val)
	}
}

// startEnvelope (re)initialises the envelope generator from the shape
// register. The envelope shape bits are CONTINUE, ATTACK, ALTERNATE, HOLD.
func (a *AY) startEnvelope(shape byte) {
	a.envContinue = (shape & 0x08) != 0
	a.envAttack = (shape & 0x04) != 0
	a.envAlt = (shape & 0x02) != 0
	a.envHold = (shape & 0x01) != 0
	a.envCounter = 0
	a.envHolding = false
	if a.envAttack {
		a.envStep = 0
	} else {
		a.envStep = 31
	}
}

// envelopeLevel returns the current 0..15 envelope output value.
func (a *AY) envelopeLevel() byte {
	step := a.envStep
	if step > 15 {
		step = 31 - step
	}
	return byte(step) & 0x0F
}

// tickEnvelope advances the envelope generator by one envelope clock tick.
func (a *AY) tickEnvelope() {
	if a.envHolding {
		return
	}
	if a.envAttack {
		a.envStep++
	} else {
		a.envStep--
	}
	if a.envStep < 0 || a.envStep > 31 {
		if !a.envContinue {
			// CONTINUE=0: envelope drops to zero and holds.
			a.envStep = 0
			a.envHolding = true
			return
		}
		if a.envHold {
			// HOLD=1: lock at the appropriate end.
			if a.envAlt {
				if a.envAttack {
					a.envStep = 0
				} else {
					a.envStep = 31
				}
			} else {
				if a.envAttack {
					a.envStep = 31
				} else {
					a.envStep = 0
				}
			}
			a.envHolding = true
			return
		}
		if a.envAlt {
			// Reverse direction.
			a.envAttack = !a.envAttack
		}
		// Restart cycle.
		if a.envAttack {
			a.envStep = 0
		} else {
			a.envStep = 31
		}
	}
}

// tonePeriod returns the 12-bit tone period for the given channel (0..2).
// A period of zero is treated identically to a period of 1, which matches the
// behaviour of the real chip.
func (a *AY) tonePeriod(ch int) int {
	low := int(a.regs[ch*2])
	high := int(a.regs[ch*2+1] & 0x0F)
	period := (high << 8) | low
	if period == 0 {
		period = 1
	}
	return period
}

// noisePeriod returns the 5-bit noise period (treating zero as one).
func (a *AY) noisePeriod() int {
	period := int(a.regs[RegNoise] & 0x1F)
	if period == 0 {
		period = 1
	}
	return period
}

// envPeriod returns the 16-bit envelope period (treating zero as one).
func (a *AY) envPeriod() int {
	period := (int(a.regs[RegEnvHigh]) << 8) | int(a.regs[RegEnvLow])
	if period == 0 {
		period = 1
	}
	return period
}

// stepClock advances the AY internal state by one "AY internal" tick. The
// AY-3-8912 internally divides its master clock by 16 before driving the
// tone and noise generators, and by 256 before driving the envelope
// generator. We pre-divide here so each call to stepClock represents one
// post-divider tick of the tone/noise generators (every 16 master cycles).
func (a *AY) stepClock() {
	// Tone generators: each programmed period is in units of "16 master
	// cycles", so we increment once per call and toggle when the counter
	// reaches the programmed period.
	for ch := 0; ch < 3; ch++ {
		a.toneCounter[ch]++
		if a.toneCounter[ch] >= a.tonePeriod(ch) {
			a.toneCounter[ch] = 0
			a.toneOutput[ch] ^= 1
		}
	}

	// Noise generator: same /16 divider as the tone channels.
	a.noiseCounter++
	if a.noiseCounter >= a.noisePeriod() {
		a.noiseCounter = 0
		// 17-bit LFSR with taps at bits 0 and 3 (matches AY-3-8910 family).
		bit := (a.noiseLFSR ^ (a.noiseLFSR >> 3)) & 1
		a.noiseLFSR = (a.noiseLFSR >> 1) | (bit << 16)
		a.noiseOutput = byte(a.noiseLFSR & 1)
	}

	// Envelope generator: an additional /16 on top of the tone/noise divider
	// gives the /256 master-clock divider the real chip uses.
	a.envCounter++
	if a.envCounter >= a.envPeriod()*16 {
		a.envCounter = 0
		a.tickEnvelope()
	}
}

// channelLevel computes the linearised output level (signed 16-bit) for one
// channel. It honours the mixer settings, channel volume register and
// envelope generator.
func (a *AY) channelLevel(ch int) int16 {
	mixer := a.regs[RegMixer]
	toneEnabled := (mixer>>uint(ch))&1 == 0
	noiseEnabled := (mixer>>uint(ch+3))&1 == 0

	tone := a.toneOutput[ch]
	if !toneEnabled {
		tone = 1 // disabled tone is held high (no modulation)
	}
	noise := a.noiseOutput
	if !noiseEnabled {
		noise = 1
	}

	// AND the tone and noise bits — if either is low the channel is silent
	// for this sample.
	output := tone & noise
	if output == 0 {
		return 0
	}

	volReg := a.regs[RegVolumeA+ch]
	var level byte
	if (volReg & 0x10) != 0 {
		// Envelope mode
		level = a.envelopeLevel()
	} else {
		level = volReg & 0x0F
	}
	return volumeTable[level]
}

// GenerateSamples generates `count` audio samples (mono, signed 16-bit). The
// caller is expected to upmix to stereo if needed.
func (a *AY) GenerateSamples(count int) []int16 {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]int16, count)
	// stepClock represents one tone-generator tick = 16 master cycles, so we
	// scale the AY clock down accordingly when computing how many ticks to
	// run per audio sample.
	cyclesPerSample := float64(AYClock) / 16.0 / float64(SampleRate)

	for i := 0; i < count; i++ {
		a.clockAccum += cyclesPerSample
		steps := int(a.clockAccum)
		a.clockAccum -= float64(steps)
		for s := 0; s < steps; s++ {
			a.stepClock()
		}

		// Mix the three channels. Sum them and divide by 3 to keep the result
		// within int16 range.
		mix := int32(a.channelLevel(0)) +
			int32(a.channelLevel(1)) +
			int32(a.channelLevel(2))
		mix /= 3
		if mix > 32767 {
			mix = 32767
		} else if mix < -32768 {
			mix = -32768
		}
		out[i] = int16(mix)
	}
	return out
}

// MixInto generates `count` samples and adds them into the supplied buffer.
// This avoids the per-call allocation of GenerateSamples for the common case
// where the audio reader wants to mix AY output into an existing beeper
// stream.
func (a *AY) MixInto(buf []int16) {
	if len(buf) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// stepClock represents one tone-generator tick = 16 master cycles, so we
	// scale the AY clock down accordingly when computing how many ticks to
	// run per audio sample.
	cyclesPerSample := float64(AYClock) / 16.0 / float64(SampleRate)
	for i := range buf {
		a.clockAccum += cyclesPerSample
		steps := int(a.clockAccum)
		a.clockAccum -= float64(steps)
		for s := 0; s < steps; s++ {
			a.stepClock()
		}

		mix := int32(a.channelLevel(0)) +
			int32(a.channelLevel(1)) +
			int32(a.channelLevel(2))
		mix /= 3

		sum := int32(buf[i]) + mix
		if sum > 32767 {
			sum = 32767
		} else if sum < -32768 {
			sum = -32768
		}
		buf[i] = int16(sum)
	}
}
