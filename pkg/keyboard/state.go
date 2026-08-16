package keyboard

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture of the key matrix, for rewind and for replaying
// execution forward from a captured point.
//
// The eight matrix bytes are the obvious part. The part that bites is the
// typed-character symbol pulse: TypeRune overlays a SYMBOL-SHIFT combination
// on the matrix for runePulseFrames frames, and neither the overlay nor the
// frames still owed to it is readable by the guest or present in any snapshot
// format. Restore the matrix alone into the middle of a pulse and the symbol
// either never reaches the ROM's scan or never lifts — a key stuck down for
// the rest of the session, which is the failure a rewind must not be able to
// cause.

// kbState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running keyboard that the guest or
// the UI can observe is present: adding a field to Keyboard without adding it
// here is the failure this package's tests are built to catch.
//
// Three fields of Keyboard are deliberately absent:
//
//   - keyMap is host-side input configuration, not emulated machine state. The
//     guest only ever sees the matrix; the map is chosen at wiring time by the
//     machine personality (UseZX8xLayout) or by the user (SetMapping,
//     LoadOverrides). A rewind that silently reverted a user's remapping would
//     be a bug, not fidelity. It is also not gob-representable as it stands,
//     since keyMapping's fields are unexported.
//   - matrixMu is a lock, not state.
//   - nmiCallback is live wiring to the CPU's NMI line, set once when the
//     machine is built and never changed by the guest. A func value cannot be
//     serialised, and there is nothing to restore: the callback the machine
//     holds after a rewind is the same one it held before.
type kbState struct {
	Matrix       [8]byte
	BreakPressed bool
	PulseMatrix  [8]byte
	PulseFrames  int
}

// StateID identifies the keyboard in a captured machine state.
func (k *Keyboard) StateID() string { return "keyboard" }

// SaveState captures the complete matrix state.
func (k *Keyboard) SaveState() []byte {
	k.matrixMu.RLock()
	s := kbState{
		Matrix:       k.matrix,
		BreakPressed: k.breakPressed,
		PulseMatrix:  k.pulseMatrix,
		PulseFrames:  k.pulseFrames,
	}
	k.matrixMu.RUnlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values cannot fail for any reason
		// the caller could act on, and returning an error here would push a
		// dead branch into every caller.
		panic("keyboard: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (k *Keyboard) LoadState(b []byte) error {
	var s kbState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("keyboard: decoding state: %w", err)
	}

	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	k.matrix = s.Matrix
	k.breakPressed = s.BreakPressed
	k.pulseMatrix = s.PulseMatrix
	k.pulseFrames = s.PulseFrames
	return nil
}
