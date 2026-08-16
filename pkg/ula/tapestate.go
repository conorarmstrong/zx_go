package ula

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete capture of the tape player's PLAYBACK POSITION, so a rewind takes
// the tape back with the machine.
//
// Without it, rewinding during a load put the CPU back and left the tape where
// it had run to, so the ROM's edge loop resumed against a part of the stream it
// had never been at: a load that had been working came back as "R Tape loading
// error". Everything that decides the next edge is here — which block the
// cursor is on, how far into that block's pulse train it is, when the last edge
// fired and against which clock, the EAR level that edge left behind, whether
// the block's bytes are already off the tape, and any TZX loop still open.
//
// # Where the line is drawn, and why
//
// The tape IMAGE is NOT captured: neither the block list nor its bytes. That is
// media — the contents of a file the user mounted — and a rewind does not
// unmount it or reload it. The same physical cassette is still in the deck
// after a rewind, which is exactly what leaving it out models. Capturing it
// would also make every rewind point carry a copy of the whole tape, so a
// 200 KB game would bound the rewind ring to a handful of points, and it would
// let a restore silently swap the user's tape for an older copy of itself.
//
// pulses and dataPulses are left out for a different reason: they are derived,
// not held. Both are a pure function of the block the cursor sits on, so
// LoadState regenerates them from blocks[pulseBlock] rather than storing them.
// That is not an optimisation at the margins — one standard 48K data block
// expands to about 800,000 pulses, which is over 3 MB per capture for a value
// that can be recomputed exactly.
//
// is48K is wiring (the ULA installs it, pointing at the live memory's model),
// and mu is the player's lock. Neither is state a rewind can restore.

// tapeState is the wire form. Every field is exported because gob only encodes
// exported fields.
type tapeState struct {
	// Where the cursor is.
	BlockIdx   int
	PulseIdx   int
	PulseBlock int

	// What decides the next edge.
	Tstate     uint64
	LastToggle uint64
	EarBit     bool

	// Whether the tape is running, and whether the current block's bytes have
	// already gone past (which is what the fast-load trap consults).
	Playing      bool
	DataConsumed bool

	// Open TZX counted loops, outermost first.
	LoopStack []stateLoopFrame
}

// stateLoopFrame mirrors the unexported tapeLoopFrame: gob would silently drop
// its fields otherwise.
type stateLoopFrame struct {
	BodyStart int
	Remaining uint16
}

// StateID identifies the tape player in a captured machine state.
func (tp *TapePlayer) StateID() string { return "tape" }

// SaveState captures the playback position.
func (tp *TapePlayer) SaveState() []byte {
	tp.mu.Lock()
	s := tapeState{
		BlockIdx:   tp.blockIdx,
		PulseIdx:   tp.pulseIdx,
		PulseBlock: tp.pulseBlock,

		Tstate:     tp.tstate,
		LastToggle: tp.lastToggle,
		EarBit:     tp.earBit,

		Playing:      tp.playing,
		DataConsumed: tp.dataConsumed,

		LoopStack: saveLoopFrames(tp.loopStack),
	}
	tp.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// A struct of fixed-size values and a slice of them cannot fail to
		// encode for any reason a caller could act on.
		panic("ula: encoding tape state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the player untouched rather than half-rewound.
func (tp *TapePlayer) LoadState(b []byte) error {
	var s tapeState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("ula: decoding tape state: %w", err)
	}

	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.blockIdx = s.BlockIdx
	tp.pulseIdx = s.PulseIdx
	tp.pulseBlock = s.PulseBlock

	tp.tstate = s.Tstate
	tp.lastToggle = s.LastToggle
	tp.earBit = s.EarBit

	tp.playing = s.Playing
	tp.dataConsumed = s.DataConsumed

	tp.loopStack = loadLoopFrames(s.LoopStack)

	// Rebuild the derived pulse train for the block the cursor is on. Update
	// only regenerates when it crosses a block boundary, so restoring pulseIdx
	// against the pulses of whatever block was playing a moment ago would
	// resume in the middle of the wrong waveform. pulseBlock is -1 when no
	// block has been generated yet (a fresh load, or a seek), and then there is
	// nothing to rebuild.
	tp.pulses, tp.dataPulses = nil, 0
	if tp.pulseBlock >= 0 && tp.pulseBlock < len(tp.blocks) {
		tp.pulses, tp.dataPulses = tp.generatePulsesData(tp.blocks[tp.pulseBlock])
	}
	return nil
}

// The loop stack is converted frame by frame rather than reinterpreted, so a
// capture never shares a backing array with the running player, which goes on
// pushing and popping frames as the tape plays.

func saveLoopFrames(in []tapeLoopFrame) []stateLoopFrame {
	if len(in) == 0 {
		return nil
	}
	out := make([]stateLoopFrame, len(in))
	for i, f := range in {
		out[i] = stateLoopFrame{BodyStart: f.bodyStart, Remaining: f.remaining}
	}
	return out
}

func loadLoopFrames(in []stateLoopFrame) []tapeLoopFrame {
	if len(in) == 0 {
		return nil
	}
	out := make([]tapeLoopFrame, len(in))
	for i, f := range in {
		out[i] = tapeLoopFrame{bodyStart: f.BodyStart, remaining: f.Remaining}
	}
	return out
}
