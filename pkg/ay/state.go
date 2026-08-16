package ay

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The guest-visible registers are the small part. What actually determines the
// next sample is the divider counters, the 17-bit noise LFSR and the
// envelope's position and direction, none of which the guest can read and none
// of which appear in any .SNA or .Z80 snapshot. A capture that stopped at the
// registers would restore a chip that runs but does not resume: same
// configuration, different phase, different sound.

// ayState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running chip is present: adding a
// field to AY without adding it here is the failure this package's tests are
// built to catch.
type ayState struct {
	Regs     [NumRegisters]byte
	Selected byte

	ToneCounter [3]int
	ToneOutput  [3]byte

	NoiseCounter int
	NoiseLFSR    uint32
	NoiseHalf    bool
	NoiseOutput  byte

	EnvCounter  int
	EnvStep     int
	EnvHolding  bool
	EnvInc      bool
	EnvPrimed   bool
	Shape       byte
	EnvContinue bool
	EnvAlt      bool
	EnvHold     bool
	EnvAttack   bool

	ClockAccum float64
}

// StateID identifies the chip in a captured machine state.
func (a *AY) StateID() string { return "ay" }

// SaveState captures the complete generator state.
func (a *AY) SaveState() []byte {
	a.mu.Lock()
	s := ayState{
		Regs:         a.regs,
		Selected:     a.selected,
		ToneCounter:  a.toneCounter,
		ToneOutput:   a.toneOutput,
		NoiseCounter: a.noiseCounter,
		NoiseLFSR:    a.noiseLFSR,
		NoiseHalf:    a.noiseHalf,
		NoiseOutput:  a.noiseOutput,
		EnvCounter:   a.envCounter,
		EnvStep:      a.envStep,
		EnvHolding:   a.envHolding,
		EnvInc:       a.envInc,
		EnvPrimed:    a.envPrimed,
		Shape:        a.shape,
		EnvContinue:  a.envContinue,
		EnvAlt:       a.envAlt,
		EnvHold:      a.envHold,
		EnvAttack:    a.envAttack,
		ClockAccum:   a.clockAccum,
	}
	a.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values cannot fail for any reason
		// the caller could act on, and returning an error here would push a
		// dead branch into every caller.
		panic("ay: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (a *AY) LoadState(b []byte) error {
	var s ayState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("ay: decoding state: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.regs = s.Regs
	a.selected = s.Selected
	a.toneCounter = s.ToneCounter
	a.toneOutput = s.ToneOutput
	a.noiseCounter = s.NoiseCounter
	a.noiseLFSR = s.NoiseLFSR
	a.noiseHalf = s.NoiseHalf
	a.noiseOutput = s.NoiseOutput
	a.envCounter = s.EnvCounter
	a.envStep = s.EnvStep
	a.envHolding = s.EnvHolding
	a.envInc = s.EnvInc
	a.envPrimed = s.EnvPrimed
	a.shape = s.Shape
	a.envContinue = s.EnvContinue
	a.envAlt = s.EnvAlt
	a.envHold = s.EnvHold
	a.envAttack = s.EnvAttack
	a.clockAccum = s.ClockAccum
	return nil
}
