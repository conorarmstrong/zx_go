package saa1099

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from a
// captured point.
//
// The 32 registers are the small part and are not what decides the next sample.
// Six tone phases, two noise phases, two
// LFSRs and two envelope positions do, and none of them is readable through any
// port. A capture that stopped at the registers would restore a chip at the
// right pitch and the wrong point in its cycle, which is a click on every
// rewind and a different waveform after it.

// saaState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running chip is present: adding a
// field to SAA without adding it here is the failure this package's tests are
// built to catch.
type saaState struct {
	Regs     [32]byte
	Selected byte

	TonePhase [6]float64

	NoisePhase [2]float64
	NoiseLFSR  [2]uint32
	NoiseHigh  [2]bool

	EnvPhase [2]float64
	EnvStep  [2]int
	EnvDone  [2]bool
}

// StateID identifies the SAA1099 in a captured machine state.
func (s *SAA) StateID() string { return "sam.saa1099" }

// SaveState captures the complete chip state.
func (s *SAA) SaveState() []byte {
	st := saaState{
		Regs:       s.regs,
		Selected:   s.selected,
		TonePhase:  s.tonePhase,
		NoisePhase: s.noisePhase,
		NoiseLFSR:  s.noiseLFSR,
		NoiseHigh:  s.noiseHigh,
		EnvPhase:   s.envPhase,
		EnvStep:    s.envStep,
		EnvDone:    s.envDone,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		// Encoding fixed-size values cannot fail in practice, but this runs
		// from the hook that drives capture, and that path deliberately skips a
		// failed capture rather than stopping the machine. A nil blob is
		// rejected by LoadState, so a bad capture surfaces as a failed restore.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded whole before any of it is applied, so a malformed one
// cannot leave the chip with its registers rewound and its generators still
// running on from the present. That mixture is audible and would be hard to
// attribute.
func (s *SAA) LoadState(blob []byte) error {
	if len(blob) == 0 {
		return fmt.Errorf("saa1099: empty state (the capture failed)")
	}
	var st saaState
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
		return fmt.Errorf("saa1099: decoding state: %w", err)
	}

	s.regs = st.Regs
	s.selected = st.Selected
	s.tonePhase = st.TonePhase
	s.noisePhase = st.NoisePhase
	s.noiseLFSR = st.NoiseLFSR
	s.noiseHigh = st.NoiseHigh
	s.envPhase = st.EnvPhase
	s.envStep = st.EnvStep
	s.envDone = st.EnvDone
	return nil
}
