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

// 4-bit volume table for the AY-3-8912 (output level for each volume value
// 0..15), commonly-cited levels measured from a real AY-3-8912 (0..0xFFFF
// full scale), scaled so the loudest channel alone tops out at 11650 — well
// below int16 saturation, leaving headroom for mixing the other two channels
// and the beeper. This is the real chip's slightly-irregular curve, not a
// uniform 3 dB-per-step approximation. The 5-bit envelope reuses these 16
// levels via envelopeLevel() & 0x0F.
var volumeTable = [16]int16{
	0, 160, 238, 336,
	466, 686, 959, 1570,
	1849, 2972, 4147, 5204,
	6570, 8251, 9813, 11650,
}

// volTableYm / volTableAy are the per-channel DAC curves taken VERBATIM from
// the FPGA core (audio/ym2149.vhd:150-162). They map the 5-bit channel mixer
// index to an 8-bit unsigned output level. The Next defaults to YM mode and
// runs the 32-entry table over the full 5-bit envelope/volume index; AY mode
// uses the 16-entry table over the top 4 bits. Modification #4 in the core
// makes index 0 (and fixed-volume 0) read as a true 0 rather than the curve's
// quiescent floor, "to produce cleaner audio out of the zx next".
//
// Exposed via ChannelDAC for the FPGA-faithful golden replay; the int16
// mixing path (GenerateSamples/MixInto) keeps its own beeper-headroom scale,
// documented at volumeTable.
var volTableYm = [32]byte{
	0x00, 0x01, 0x01, 0x02, 0x02, 0x03, 0x03, 0x04,
	0x06, 0x07, 0x09, 0x0a, 0x0c, 0x0e, 0x11, 0x13,
	0x17, 0x1b, 0x20, 0x25, 0x2c, 0x35, 0x3e, 0x47,
	0x54, 0x66, 0x77, 0x88, 0xa1, 0xc0, 0xe0, 0xff,
}

var volTableAy = [16]byte{
	0x00, 0x03, 0x04, 0x06, 0x0a, 0x0f, 0x15, 0x22,
	0x28, 0x41, 0x5b, 0x72, 0x90, 0xb5, 0xd7, 0xff,
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

	// Noise generator state. The FPGA noise generator (audio/ym2149.vhd:281-302)
	// is a 17-bit LFSR (poly17) clocked at HALF the tone-generator rate
	// (ena_div_noise = ena_div/2). noiseHalf tracks that /2 toggle. The LFSR
	// powers up all-zeros; the feedback re-injects a 1 when the register is all
	// zero (poly17_zero), so it self-starts. Taps are bits 0 and 2.
	noiseCounter int
	noiseLFSR    uint32 // 17-bit LFSR (poly17) for the noise output
	noiseHalf    bool   // FPGA noise_div: noise advances on every 2nd tone tick
	noiseOutput  byte   // 0 or 1

	// Envelope generator. Modelled as the FPGA's 5-bit env_vol (0..31) state
	// machine (audio/ym2149.vhd:359-465). envStep mirrors env_vol; envInc is
	// the FPGA's env_inc direction bit (true = +1 / rising); envHolding is
	// env_hold. The shape byte is latched in shape.
	envCounter  int
	envStep     int  // env_vol, 0..31
	envHolding  bool // env_hold: envelope locked at an end value
	envInc      bool // env_inc: current step direction (true = +1)
	envPrimed   bool // env_reset primes env_ena=1, giving one immediate step
	shape       byte // the latched NR$13 shape byte (bits C/Att/Alt/Hold)
	envContinue bool
	envAlt      bool
	envHold     bool
	envAttack   bool

	// Sub-counters for clock division. The AY is clocked at AYClock Hz; this
	// counter accumulates fractional cycles per audio sample so we can advance
	// the internal state by an exact integer number of master cycles.
	clockAccum float64
}

// New creates a new AY instance with all registers cleared. The noise LFSR
// powers up all-zeros, matching the FPGA core (audio/ym2149.vhd:111); the
// feedback's poly17_zero term self-starts it.
func New() *AY {
	a := &AY{}
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
	a.noiseLFSR = 0
	a.noiseHalf = false
	a.noiseOutput = 0
	a.envCounter = 0
	a.envStep = 0
	a.envHolding = false
	a.envInc = false
	a.envPrimed = false
	a.shape = 0
	a.envAttack = false
	a.envContinue = false
	a.envAlt = false
	a.envHold = false
	a.clockAccum = 0

	// Default mixer: all channels muted (bits 0-5 high = disabled).
	a.regs[RegMixer] = 0x3F
}

// ResetRegisters mirrors the FPGA core's hardware RESET_H exactly
// (audio/ym2149.vhd:170-186, 526): it clears the 16 registers (with reg 7 =
// 0xFF, all tone+noise disabled), the selected-register latch and the DAC
// outputs — but DELIBERATELY leaves the tone/noise/envelope GENERATORS
// running. On the real chip RESET_H does not touch poly17, the tone/noise/env
// counters or env_vol, so a reset mid-stream does not re-seed the noise LFSR or
// reset the square-wave phase. This is the faithful reset for the FPGA golden
// replay (where one continuously-running chip is reset between sequences).
func (a *AY) ResetRegisters() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.regs {
		a.regs[i] = 0
	}
	a.regs[RegMixer] = 0xFF
	a.selected = 0
}

// SelectRegister latches the register that will be read or written via the
// data port. Only the low 4 bits are significant.
func (a *AY) SelectRegister(reg byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.selected = reg & 0x0F
}

// Selected returns the index of the currently-selected AY register
// (0..15). Useful for snapshot save and for tests that need to
// confirm a port-0xFFFD write reached the right chip in a
// multi-AY configuration.
func (a *AY) Selected() byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selected
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
// register. This is the FPGA env_reset load (audio/ym2149.vhd:392-402): on a
// shape write, ATTACK=0 (decay) loads env_vol=31 going down; ATTACK=1 (attack)
// loads env_vol=0 going up. env_hold is cleared so the envelope re-runs.
//
// The shape bits are CONTINUE(3), ATTACK(2), ALTERNATE(1), HOLD(0). They are
// consulted live from a.shape inside tickEnvelope, exactly as the FPGA reads
// reg(13) bits there.
func (a *AY) startEnvelope(shape byte) {
	a.shape = shape & 0x0F
	a.envContinue = (shape & 0x08) != 0
	a.envAttack = (shape & 0x04) != 0
	a.envAlt = (shape & 0x02) != 0
	a.envHold = (shape & 0x01) != 0
	a.envCounter = 0
	a.envHolding = false
	// env_reset sets env_ena=1 (ym2149.vhd:342), which the shape process
	// consumes as one immediate step on the next ENA tick — independent of the
	// envelope-period divider. We replay that as a one-shot priming step.
	a.envPrimed = true
	if a.envAttack {
		a.envStep = 0
		a.envInc = true // +1
	} else {
		a.envStep = 31
		a.envInc = false // -1
	}
}

// envVol5 returns the full 5-bit (0..31) envelope output value used by the YM
// DAC. This is the FPGA env_vol directly.
func (a *AY) envVol5() byte { return byte(a.envStep) & 0x1F }

// envelopeLevel returns the 4-bit (0..15) envelope output value. The AY-mode
// DAC and the legacy 16-entry mixing path use env_vol's top four bits
// (audio/ym2149.vhd:535-537: env_vol(4 downto 1)), i.e. env_vol >> 1.
func (a *AY) envelopeLevel() byte {
	return byte(a.envStep>>1) & 0x0F
}

// tickEnvelope advances the envelope by one envelope clock, a faithful port of
// the FPGA env shape process (audio/ym2149.vhd:369-465). It mirrors the
// hardware PHASE exactly: the step and the env_hold/env_inc updates are all
// evaluated against the CURRENT env_vol and applied together at the tick (the
// FPGA's non-blocking assignments). env_hold therefore engages on the tick
// AFTER its predicate matched, suppressing that next step — which is why, e.g.,
// a decay shape that flags hold at env_vol==1 (is_bot_p1) comes to rest at 0.
//
// Edge predicates on the current env_vol (ym2149.vhd:361-367):
//
//	is_bot    = env_vol == 0
//	is_bot_p1 = env_vol == 1
//	is_top_m1 = env_vol == 30
//	is_top    = env_vol == 31
func (a *AY) tickEnvelope() {
	// Predicates evaluated on the CURRENT (pre-step) env_vol.
	cur := a.envStep
	isBot := cur == 0
	isBotP1 := cur == 1
	isTopM1 := cur == 30
	isTop := cur == 31

	// The STEP is gated by env_hold (ym2149.vhd:404), but the shape control
	// below ALWAYS runs — that is how the alternating shapes flip direction out
	// of a one-tick hold at the edge.
	if !a.envHolding {
		if a.envInc {
			a.envStep = (a.envStep + 1) & 0x1F
		} else {
			a.envStep = (a.envStep - 1) & 0x1F
		}
	}

	cont := a.shape&0x08 != 0
	alt := a.shape&0x02 != 0
	hold := a.shape&0x01 != 0

	// cont=1, hold=0, alt=0 → sawtooth shapes 8/12: no branch matches, so
	// env_vol just keeps stepping. The 5-bit wrap (31→0→31 down, 31 wrap up→0)
	// produces the repeating ramp on its own — exactly as the FPGA leaves that
	// register-bit combination with no env_hold/env_inc action.
	switch {
	case !cont:
		// reg(13)(3)='0': shapes 0-7. Decay holds at is_bot_p1, attack at is_top.
		if !a.envInc { // down
			if isBotP1 {
				a.envHolding = true
			}
		} else { // up
			if isTop {
				a.envHolding = true
			}
		}
	case hold:
		// reg(13)(0)='1': one-shot hold shapes 9/11/13/15.
		if !a.envInc { // down
			if alt {
				if isBot {
					a.envHolding = true
				}
			} else if isBotP1 {
				a.envHolding = true
			}
		} else { // up
			if alt {
				if isTop {
					a.envHolding = true
				}
			} else if isTopM1 {
				a.envHolding = true
			}
		}
	case alt:
		// reg(13)(1)='1', hold=0: alternating triangle shapes 10/14. The hold is
		// transient — set at the penultimate step, then cleared with a direction
		// flip at the edge (so the envelope reverses rather than stopping).
		if !a.envInc { // down
			if isBotP1 {
				a.envHolding = true
			}
			if isBot {
				a.envHolding = false
				a.envInc = true
			}
		} else { // up
			if isTopM1 {
				a.envHolding = true
			}
			if isTop {
				a.envHolding = false
				a.envInc = false
			}
		}
	}
}

// toneComp returns the FPGA tone-generator compare value for a channel
// (audio/ym2149.vhd:310-312): freq-1 when the period's upper 11 bits are
// nonzero (freq >= 2), else 0. The generator toggles when its counter reaches
// this value, so comp 0 means "toggle every tick" (the freq 0/1 fast case).
func (a *AY) toneComp(ch int) int {
	freq := (int(a.regs[ch*2+1]&0x0F) << 8) | int(a.regs[ch*2])
	if freq>>1 != 0 {
		return freq - 1
	}
	return 0
}

// noiseComp returns the FPGA noise-generator compare value
// (audio/ym2149.vhd:283): period-1 when bits 4:1 are nonzero (period >= 2),
// else 0.
func (a *AY) noiseComp() int {
	p := int(a.regs[RegNoise] & 0x1F)
	if p>>1 != 0 {
		return p - 1
	}
	return 0
}

// envComp returns the FPGA envelope-period compare value
// (audio/ym2149.vhd:335): period-1 when bits 15:1 are nonzero (period >= 2),
// else 0.
func (a *AY) envComp() int {
	p := (int(a.regs[RegEnvHigh]) << 8) | int(a.regs[RegEnvLow])
	if p>>1 != 0 {
		return p - 1
	}
	return 0
}

// stepClock advances the AY internal state by one "AY internal" tick. The
// AY-3-8912 internally divides its master clock by 16 before driving the
// tone and noise generators, and by 256 before driving the envelope
// generator. We pre-divide here so each call to stepClock represents one
// post-divider tick of the tone/noise generators (every 16 master cycles).
func (a *AY) stepClock() {
	// Tone generators (audio/ym2149.vhd:314-330): toggle when the counter
	// reaches comp = period-1 (or 0 for the period 0/1 fast case), then reset.
	for ch := 0; ch < 3; ch++ {
		if a.toneCounter[ch] >= a.toneComp(ch) {
			a.toneCounter[ch] = 0
			a.toneOutput[ch] ^= 1
		} else {
			a.toneCounter[ch]++
		}
	}

	// Noise generator (audio/ym2149.vhd:259-302). It runs at HALF the tone-tick
	// rate. The FPGA toggles noise_div every tone tick and asserts ena_div_noise
	// when the noise_div value BEFORE the toggle was 1 (lines 270-273) — so with
	// noise_div powering up at 0, the LFSR advances on the 2nd, 4th, 6th… tone
	// tick. noiseHalf mirrors noise_div: we read it, then toggle it, advancing
	// the LFSR on the read-as-true cycle.
	enaDivNoise := a.noiseHalf
	a.noiseHalf = !a.noiseHalf
	if enaDivNoise {
		if a.noiseCounter >= a.noiseComp() {
			a.noiseCounter = 0
			// 17-bit LFSR (poly17), taps at bits 0 and 2, with the all-zero
			// re-injection (poly17_zero) that self-starts it from the cold
			// all-zero power-on state.
			zero := uint32(0)
			if a.noiseLFSR == 0 {
				zero = 1
			}
			fb := ((a.noiseLFSR & 1) ^ ((a.noiseLFSR >> 2) & 1) ^ zero) & 1
			a.noiseLFSR = (a.noiseLFSR >> 1) | (fb << 16)
			a.noiseOutput = byte(a.noiseLFSR & 1)
		} else {
			a.noiseCounter++
		}
	}

	// Envelope generator (audio/ym2149.vhd:332-357): env_gen_cnt counts tone
	// ticks (ena_div); env_ena pulses — advancing the envelope shape — when it
	// reaches comp = env_period-1. So the envelope steps once every env_period
	// tone ticks (NOT period*16 — the FPGA core runs the envelope divider off
	// the same /16 ena_div as the tone generators).
	//
	// The env_reset priming (envPrimed) adds one immediate step on the first
	// tick after a shape write, on top of any divider hit that same tick. For
	// env period 1 (comp 0) that yields the two-step head start the hardware
	// shows; for longer periods it is a single initial step before the cadence
	// settles.
	fired := a.envCounter >= a.envComp()
	if fired {
		a.envCounter = 0
	} else {
		a.envCounter++
	}
	if a.envPrimed {
		a.tickEnvelope()
		a.envPrimed = false
	}
	if fired {
		a.tickEnvelope()
	}
}

// channelIndex5 computes the 5-bit channel mixer index (0..31) for one channel,
// a faithful port of the FPGA mixer (audio/ym2149.vhd:467-516). It is the value
// fed into the DAC volume table:
//
//   - The channel is gated by chan_mixed = (toneDisable OR toneOut) AND
//     (noiseDisable OR noiseOut). If gated off → 0.
//   - In fixed-volume mode (volReg bit4=0): index = vol<<1 | 1 (i.e. vol*2+1),
//     EXCEPT vol 0 which the core forces to 0 (modification #4).
//   - In envelope mode (volReg bit4=1): index = the full 5-bit env_vol.
func (a *AY) channelIndex5(ch int) byte {
	mixer := a.regs[RegMixer]
	toneDisable := (mixer >> uint(ch)) & 1
	noiseDisable := (mixer >> uint(ch+3)) & 1

	tone := toneDisable | a.toneOutput[ch]
	noise := noiseDisable | a.noiseOutput
	if (tone & noise) == 0 {
		return 0
	}

	volReg := a.regs[RegVolumeA+ch]
	if volReg&0x10 != 0 {
		return a.envVol5()
	}
	v := volReg & 0x0F
	if v == 0 {
		return 0
	}
	return (v << 1) | 1
}

// ChannelDAC returns the 8-bit unsigned per-channel digital output level
// (O_AUDIO_A/B/C) for channel ch (0..2), exactly as the FPGA core's DAC
// produces it (audio/ym2149.vhd:522-541). aymode selects the curve: false = YM
// (the Next default; 32-entry table over the full 5-bit index) and true = AY
// (16-entry table over the index's top 4 bits). This is the chip's true digital
// output and is what the FPGA golden replay asserts against.
func (a *AY) ChannelDAC(ch int, aymode bool) byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.channelDACLocked(ch, aymode)
}

func (a *AY) channelDACLocked(ch int, aymode bool) byte {
	idx := a.channelIndex5(ch)
	if aymode {
		return volTableAy[idx>>1]
	}
	return volTableYm[idx]
}

// StepTick advances the chip by exactly one tone-generator tick (one ena_div),
// the same unit the FPGA golden samples. Exposed for the FPGA-faithful replay
// test; production audio uses the internal stepClock via GenerateSamples.
func (a *AY) StepTick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stepClock()
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

// cyclesPerSample returns the number of stepClock ticks (post /16 divider)
// per output audio sample. Shared by GenerateSamples and MixInto.
func cyclesPerSample() float64 {
	return float64(AYClock) / 16.0 / float64(SampleRate)
}

// advanceSample steps the internal clock by the fractional number of AY ticks
// due for one audio sample (accumulating leftover fractional cycles in
// clockAccum) and returns the summed three-channel mix, unclamped. Must be
// called with the mutex held.
func (a *AY) advanceSample(cps float64) int32 {
	a.clockAccum += cps
	steps := int(a.clockAccum)
	a.clockAccum -= float64(steps)
	for s := 0; s < steps; s++ {
		a.stepClock()
	}
	// Sum the three channels and divide by 3 to keep the result within
	// int16 range.
	return (int32(a.channelLevel(0)) +
		int32(a.channelLevel(1)) +
		int32(a.channelLevel(2))) / 3
}

func clampSample(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// GenerateSamples generates `count` audio samples (mono, signed 16-bit). The
// caller is expected to upmix to stereo if needed.
func (a *AY) GenerateSamples(count int) []int16 {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]int16, count)
	cps := cyclesPerSample()
	for i := 0; i < count; i++ {
		out[i] = clampSample(a.advanceSample(cps))
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

	cps := cyclesPerSample()
	for i := range buf {
		mix := a.advanceSample(cps)
		buf[i] = clampSample(int32(buf[i]) + mix)
	}
}
