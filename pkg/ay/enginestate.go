package ay

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture for the TurboSound engine.
//
// The Engine is the device, not its three chips. Registering the chips
// individually would need three distinct identifiers and would still miss the
// state that lives on the Engine itself: which chip port $FFFD has selected,
// and whether NextReg $06 is holding them all in reset. Neither is readable
// from any chip, so a rewind that restored only the chips would resume writing
// registers to whichever chip happened to be selected now.

// engineState is the wire form. Chips carries each generator's own blob rather
// than duplicating its fields, so adding state to AY cannot silently fall out
// of the engine's capture.
type engineState struct {
	Chips    [3][]byte
	Selected byte
	Disabled bool

	// Panning, from NR$08 bit 5 and NR$09 bits 7:5. It is captured here rather
	// than read back from the register file because nextregs.LoadState assigns
	// its register array directly without firing any OnWrite handler, so a
	// rewind moves NR$08 without anything downstream hearing about it. The
	// chips are re-told on restore, so the Engine stays the single owner.
	ACB      bool
	MonoMask byte
}

// StateID identifies the engine in a captured machine state.
func (e *Engine) StateID() string { return "ay.turbosound" }

// SaveState captures all three generators and the engine's own selection.
func (e *Engine) SaveState() []byte {
	var s engineState
	for i := range e.chips {
		s.Chips[i] = e.chips[i].SaveState()
		if s.Chips[i] == nil {
			// One chip failed to encode. Reporting an empty engine blob is
			// better than a partial one: LoadState rejects it, so the failure
			// surfaces at restore rather than as two chips resuming and one
			// carrying on from the present.
			return nil
		}
	}
	// Under the lock for the same reason MixIntoStereo reads them under it: a
	// capture is taken on the emulator goroutine while the audio callback is
	// mixing.
	e.mu.Lock()
	s.Selected = e.selected
	s.Disabled = e.disabled
	s.ACB = e.acb
	s.MonoMask = e.monoMask
	e.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// Every chip is decoded before any is applied, so a malformed blob cannot
// leave the engine with some generators rewound and others running on from the
// present. That mixture is audible and would be hard to attribute.
func (e *Engine) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("ay: empty engine state (the capture failed)")
	}
	var s engineState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("ay: decoding engine state: %w", err)
	}

	staged := [3]ayState{}
	for i := range s.Chips {
		if len(s.Chips[i]) == 0 {
			return fmt.Errorf("ay: engine state has nothing for chip %d", i)
		}
		if err := gob.NewDecoder(bytes.NewReader(s.Chips[i])).Decode(&staged[i]); err != nil {
			return fmt.Errorf("ay: decoding engine chip %d: %w", i, err)
		}
	}

	for i := range staged {
		e.chips[i].apply(staged[i])
	}
	e.mu.Lock()
	e.selected = s.Selected
	e.disabled = s.Disabled
	e.acb = s.ACB
	e.monoMask = s.MonoMask
	e.mu.Unlock()
	// Push the restored panning back down, since apply() rebuilds each chip
	// from its own blob and the mode is not part of it. Outside the lock:
	// applyPanning takes it itself, and sync.Mutex is not reentrant.
	e.applyPanning()
	return nil
}
