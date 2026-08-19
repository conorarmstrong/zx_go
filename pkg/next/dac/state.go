package dac

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The four channel values are the small part, and they are not even what the
// frame in flight is made of. Each port write only appends a timed event
// holding the output pair at that T-state; nothing reaches the audio until
// GenerateFrameStereo integrates the events and folds the last one into the
// pair carried into the next frame. A capture taken part way through a frame
// that stopped at the four channels would restore a bank missing every write
// since the last frame boundary, and missing the level it was sitting at before
// them: it would run, at the right volume, playing a different note.
//
// The channels still have to be captured, for the frames that come after. Every
// event records the mean of a channel pair (LevelL / LevelR), so a channel the
// guest has not written for a while sets half the level of the next event on
// its partner — restoring three of the four is audible on the very next write.
//
// SounDrive is deliberately not part of this capture. It is a standalone
// reference model of audio/soundrive.vhd that nothing constructs (the live port
// decode still uses Bank's fixed map, see its doc comment), so it holds no
// machine state to capture and Bank does not own one. When it does replace
// Bank's decode, its four channels, its seven mode enables and DACEnable all
// have to join this struct.

// eventState is the wire form of one timed write. The running type's fields are
// unexported and gob only encodes exported fields, so the event buffer needs a
// parallel form rather than being encoded directly.
type eventState struct {
	TStateOffset int
	Left         byte
	Right        byte
}

// bankState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running bank is present: adding a
// field to Bank without adding it here is the failure this package's tests are
// built to catch.
type bankState struct {
	Levels [4]byte
	StartL byte
	StartR byte
	Events []eventState
}

// StateID identifies the DAC bank in a captured machine state.
func (b *Bank) StateID() string { return "next.dac" }

// SaveState captures the complete bank state.
func (b *Bank) SaveState() []byte {
	s := bankState{
		Levels: b.levels,
		StartL: b.startL,
		StartR: b.startR,
		Events: make([]eventState, len(b.events)),
	}
	for i, e := range b.events {
		s.Events[i] = eventState{TStateOffset: e.tstateOffset, Left: e.left, Right: e.right}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size values cannot fail in practice, but this runs
		// from the CPU pre-fetch hook that drives capture, and that path
		// deliberately skips a failed capture rather than stopping the machine.
		// A nil blob is rejected by LoadState, so a bad capture surfaces as a
		// failed restore instead of taking the emulator down mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded whole before any of it is applied, so a malformed one
// cannot leave the bank with its channels rewound and its pending writes still
// running on from the present. That mixture is audible and would be hard to
// attribute.
func (b *Bank) LoadState(blob []byte) error {
	if len(blob) == 0 {
		return fmt.Errorf("dac: empty state (the capture failed)")
	}
	var s bankState
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&s); err != nil {
		return fmt.Errorf("dac: decoding state: %w", err)
	}

	b.levels = s.Levels
	b.startL = s.StartL
	b.startR = s.StartR
	b.events = b.events[:0]
	for _, e := range s.Events {
		b.events = append(b.events, dacEvent{tstateOffset: e.TStateOffset, left: e.Left, right: e.Right})
	}
	return nil
}
