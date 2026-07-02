// Package saa1099 emulates the Philips SAA1099 — the SAM Coupé's stereo sound
// chip: six tone channels (two groups of three) with per-channel left/right
// amplitude, two noise generators, two envelope generators, and a master
// enable/reset. Registers are written via an address-latch + data protocol.
//
// Modelled directly from the SAA1099 datasheet — no open hardware-verified
// reference core exists for this chip to cross-check against. Synthesis is at
// the audio sample rate using phase accumulators.
package saa1099

const (
	// MasterClock is the SAA1099 oscillator on the SAM Coupé (8 MHz).
	MasterClock = 8_000_000
	// SampleRate is the output rate (matches the audio system).
	SampleRate = 44100
	// toneBase = MasterClock/512; tone f = toneBase·2^octave/(511−freq).
	toneBase = MasterClock / 512 // 15625

	// volumeGain converts a summed 0..90 amplitude into 16-bit range; chosen so
	// a few channels mix loudly without the common case clipping.
	volumeGain = 512
)

// Register indices (the value written to the address latch).
const (
	regAmp0    = 0x00 // 0x00-0x05: per-channel amplitude (L=bits0-3, R=bits4-7)
	regFreq0   = 0x08 // 0x08-0x0D: per-channel frequency
	regOctave0 = 0x10 // 0x10-0x12: octave pairs (lo bits0-2, hi bits4-6)
	regFreqEn  = 0x14 // tone enable, bits 0-5
	regNoiseEn = 0x15 // noise enable, bits 0-5
	regNoise   = 0x16 // noise generator rates (gen0 bits0-1, gen1 bits4-5)
	regEnv0    = 0x18 // envelope gen 0 (controls channel 2)
	regEnv1    = 0x19 // envelope gen 1 (controls channel 5)
	regMaster  = 0x1C // bit0 = sound enable, bit1 = reset/sync
)

// SAA is a SAA1099 instance.
type SAA struct {
	regs     [32]byte
	selected byte

	tonePhase [6]float64

	noisePhase [2]float64
	noiseLFSR  [2]uint32
	noiseHigh  [2]bool

	envPhase [2]float64
	envStep  [2]int  // position within the envelope cycle
	envDone  [2]bool // single-shot shapes latch here
}

// New returns a reset SAA1099 (silent until the master enable bit is set).
func New() *SAA {
	s := &SAA{}
	s.noiseLFSR[0] = 1
	s.noiseLFSR[1] = 1
	return s
}

// Reset returns the chip to power-on state (all registers clear → silent).
func (s *SAA) Reset() {
	*s = SAA{}
	s.noiseLFSR[0] = 1
	s.noiseLFSR[1] = 1
}

// WriteAddress latches the register selected for the next data write.
func (s *SAA) WriteAddress(reg byte) { s.selected = reg & 0x1F }

// WriteData writes to the latched register.
func (s *SAA) WriteData(val byte) { s.WriteRegister(s.selected, val) }

// WriteRegister writes val to register reg directly.
func (s *SAA) WriteRegister(reg, val byte) {
	reg &= 0x1F
	s.regs[reg] = val
	switch reg {
	case regMaster:
		if val&0x02 != 0 { // reset/sync: halt + zero the generators
			s.tonePhase = [6]float64{}
			s.noisePhase = [2]float64{}
			s.envPhase = [2]float64{}
		}
	case regEnv0:
		s.envStep[0], s.envPhase[0], s.envDone[0] = 0, 0, false
	case regEnv1:
		s.envStep[1], s.envPhase[1], s.envDone[1] = 0, 0, false
	}
}

// ReadRegister returns the stored value of a register.
func (s *SAA) ReadRegister(reg byte) byte { return s.regs[reg&0x1F] }

func (s *SAA) octave(c int) int {
	r := s.regs[regOctave0+c/2]
	if c&1 == 0 {
		return int(r & 0x07)
	}
	return int((r >> 4) & 0x07)
}

// ToneFrequency returns channel c's tone frequency in Hz.
func (s *SAA) ToneFrequency(c int) float64 {
	return float64(toneBase) * float64(int(1)<<s.octave(c)) / float64(511-int(s.regs[regFreq0+c]))
}

func (s *SAA) noiseRate(g int) float64 {
	switch (s.regs[regNoise] >> (uint(g) * 4)) & 0x03 {
	case 0:
		return MasterClock / 256.0 // 31250 Hz
	case 1:
		return MasterClock / 512.0 // 15625 Hz
	case 2:
		return MasterClock / 1024.0 // 7812.5 Hz
	default:
		return s.ToneFrequency(g * 3) // clocked by the group's channel 0/3
	}
}

func (s *SAA) stepNoise(g int) {
	s.noisePhase[g] += s.noiseRate(g) / SampleRate
	for s.noisePhase[g] >= 1 {
		s.noisePhase[g] -= 1
		l := s.noiseLFSR[g]
		fb := ((l >> 16) ^ (l >> 13)) & 1 // 17-bit maximal LFSR
		l = ((l << 1) | fb) & 0x1FFFF
		s.noiseLFSR[g] = l
		s.noiseHigh[g] = l&1 != 0
	}
}

func (s *SAA) envEnabled(g int) bool { return s.regs[regEnv0+g]&0x80 != 0 }

// envExternalClock reports the envelope clock source (bit 5): set ⇒ the
// envelope is advanced by the external clock pin, which the SAM does not feed,
// so the internal tone-driven clock must not advance it.
func (s *SAA) envExternalClock(g int) bool { return s.regs[regEnv0+g]&0x20 != 0 }

// envResolution3bit reports the resolution bit (bit 4): set ⇒ 3-bit
// resolution, achieved by masking the level's least-significant bit.
func (s *SAA) envResolution3bit(g int) bool { return s.regs[regEnv0+g]&0x10 != 0 }

// envLevel returns the current 0-15 envelope level for generator g.
func (s *SAA) envLevel(g int) int {
	shape := (s.regs[regEnv0+g] >> 1) & 0x07
	step := s.envStep[g]
	var level int
	switch shape {
	case 1: // maximum amplitude
		level = 15
	case 2, 3: // single / repetitive decay (15 → 0)
		level = 15 - (step & 0x0F)
	case 4, 5: // single / repetitive triangle (0 → 15 → 0)
		if step < 16 {
			level = step
		} else {
			level = 31 - step
		}
	case 6, 7: // single / repetitive attack (0 → 15, rising ramp)
		level = step & 0x0F
	default: // 0 → off
		level = 0
	}
	if s.envResolution3bit(g) {
		level &^= 1 // 3-bit resolution: mask the LSB
	}
	return level
}

func (s *SAA) stepEnv(g int) {
	if !s.envEnabled(g) || s.envDone[g] || s.envExternalClock(g) {
		return
	}
	// Internal clock: the group's middle channel (1 / 4) tone generator.
	s.envPhase[g] += s.ToneFrequency(g*3+1) / SampleRate
	for s.envPhase[g] >= 1 {
		s.envPhase[g] -= 1
		shape := (s.regs[regEnv0+g] >> 1) & 0x07
		s.envStep[g]++
		switch shape {
		case 2: // single decay
			if s.envStep[g] >= 16 {
				s.envStep[g] = 15
				s.envDone[g] = true
			}
		case 6: // single attack
			if s.envStep[g] >= 16 {
				s.envStep[g] = 15
				s.envDone[g] = true
			}
		case 4: // single triangle
			if s.envStep[g] >= 31 {
				s.envStep[g] = 31
				s.envDone[g] = true
			}
		case 3, 7: // repetitive decay / attack
			s.envStep[g] &= 0x0F
		case 5: // repetitive triangle
			if s.envStep[g] >= 32 {
				s.envStep[g] = 0
			}
		}
	}
}

// GenerateStereo fills buf with interleaved left/right 16-bit samples (length
// must be even). Silent unless the master enable bit (reg 0x1C bit 0) is set.
func (s *SAA) GenerateStereo(buf []int16) {
	enabled := s.regs[regMaster]&0x01 != 0
	for i := 0; i+1 < len(buf); i += 2 {
		if !enabled {
			buf[i], buf[i+1] = 0, 0
			continue
		}
		s.stepNoise(0)
		s.stepNoise(1)
		s.stepEnv(0)
		s.stepEnv(1)

		var left, right int
		for c := 0; c < 6; c++ {
			s.tonePhase[c] += s.ToneFrequency(c) / SampleRate
			if s.tonePhase[c] >= 1 {
				s.tonePhase[c] -= 1
			}
			toneHigh := s.tonePhase[c] < 0.5
			g := c / 3
			freqEn := s.regs[regFreqEn]&(1<<uint(c)) != 0
			noiseEn := s.regs[regNoiseEn]&(1<<uint(c)) != 0
			if (!freqEn || !toneHigh) && (!noiseEn || !s.noiseHigh[g]) {
				continue
			}
			amp := s.regs[regAmp0+c]
			ampL, ampR := int(amp&0x0F), int(amp>>4)
			// Channels 2 and 5 are amplitude-controlled by their envelope gen.
			if c == 2 && s.envEnabled(0) {
				ampL, ampR = s.envApplied(0)
			} else if c == 5 && s.envEnabled(1) {
				ampL, ampR = s.envApplied(1)
			}
			left += ampL
			right += ampR
		}
		buf[i] = clamp16(left * volumeGain)
		buf[i+1] = clamp16(right * volumeGain)
	}
}

// envApplied returns the envelope-controlled L/R amplitude; the right channel
// is inverted when the envelope's L/R-mirror bit (bit 0) is set.
func (s *SAA) envApplied(g int) (l, r int) {
	e := s.envLevel(g)
	if s.regs[regEnv0+g]&0x01 != 0 {
		return e, 15 - e
	}
	return e, e
}

func clamp16(v int) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
