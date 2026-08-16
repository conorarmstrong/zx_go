package nextregs

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The register file is the Next's largest single piece of state and, at 256
// bytes plus a latch, also its simplest to capture. What is not simple is
// putting it back. Roughly half the file has a write side effect: NR$02
// triggers a machine reset, NR$07 changes the CPU clock, NR$50-$57 repage
// memory under the running program, NR$75-$79 advance the sprite index, NR$62
// loads the Copper. Restoring by pushing the captured bytes back in through
// the dispatcher — the obvious way, and the way a snapshot loader does it —
// would fire every one of those on every rewind, so the machine would reset
// itself instead of resuming.
//
// So LoadState assigns d.regs and d.selected directly and calls nothing:
// not write(), not WriteReg, not WriteData, not Store. The captured byte for a
// register IS the stored byte (handlers that transform a write call Store with
// the canonical value, so what we captured is already post-transform), which
// makes a direct assignment both the faithful restore and the only one that
// does not re-fire anything. It is also the only restore that works for the
// registers write() refuses outright: NR$01 and NR$0E are read-only and are
// seeded by internal Store calls, so a byte pushed back through write() would
// be silently dropped and the core version would revert.
//
// Deliberately NOT captured:
//
//   - onWrite, onRead and onReset. These are the subsystem wiring installed
//     once at construction by pkg/next.Wire; no guest access can change them,
//     and gob cannot encode a func in any case. A rewind moves the machine
//     through time, it must not re-point it at different objects.
//   - tracer. Debug instrumentation owned by whoever installed it, not
//     machine state. Restoring one would either detach a live trace or
//     resurrect a dead one.
//   - Everything the handlers own on the far side of those callbacks: the
//     palette bank's index and 9-bit pairing latch, the sprite engine's
//     selected sprite, the MMU's slot banks, the CPU's speed select, the
//     Copper's program and position. None of it lives in this register file;
//     it lives in the subsystems, which capture it themselves. Capturing it
//     from here would either duplicate those captures or, worse, restore them
//     in this device's order rather than their own.
//
// The one thing genuinely out of reach is subsystem state held in closures
// inside pkg/next/wire.go rather than in a subsystem object: the four clip
// windows' index and coordinates (NR$18-$1C), and NR$02's 3-bit reset-type
// shift history, whose top bit software never sees and which therefore cannot
// be reconstructed from the stored NR$02 byte. Those are unreachable from
// here and from any Device; they need moving into a struct with its own
// capture before a rewind reproduces them.

// nextRegsState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running dispatcher that is
// data rather than wiring is present: adding a value field to Dispatcher
// without adding it here is the failure this package's tests are built to
// catch.
type nextRegsState struct {
	Regs     [256]byte
	Selected byte
}

// StateID identifies the register file in a captured machine state.
func (d *Dispatcher) StateID() string { return "next.nextregs" }

// SaveState captures the complete register file.
func (d *Dispatcher) SaveState() []byte {
	s := nextRegsState{
		Regs:     d.regs,
		Selected: d.selected,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size values cannot fail in practice, but this runs
		// from the CPU pre-fetch hook that drives capture, and that path
		// deliberately skips a failed capture rather than stopping the
		// machine. A nil blob is rejected by LoadState, so a bad capture
		// surfaces as a failed restore instead of a crash mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded in full into a local before any of it is applied, so a
// truncated or malformed state leaves the register file untouched rather than
// half-rewound — a half-rewound register file still runs, which is exactly why
// it has to be impossible.
func (d *Dispatcher) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("nextregs: empty state (the capture failed)")
	}
	var s nextRegsState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("nextregs: decoding state: %w", err)
	}

	// Direct assignment, not a replay through write(): see the file comment.
	d.regs = s.Regs
	d.selected = s.Selected
	return nil
}
