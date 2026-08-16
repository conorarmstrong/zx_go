// Package machinestate captures and restores the complete state of an
// emulated machine, so that execution can be resumed from a captured point and
// produce exactly what it produced the first time.
//
// It exists because a partial capture does not fail loudly. Restore the CPU
// and the RAM but forget the sound chip's envelope counter, and the machine
// still runs; it just stops being the machine you rewound, in a way that only
// shows up as a wrong note or a disk read that returns the wrong sector.
// Everything here is built so that an omission is detectable:
//
//   - Every device is named, and Restore refuses a state whose device set does
//     not match the machine exactly, in either direction. A device that joined
//     the machine after the capture is an error, not a device left holding
//     present-day values.
//   - A capture is ordered by name and encodes to canonical bytes, so two
//     captures of an unchanged machine compare equal. That is what lets a test
//     assert "replaying from here produced the same machine" rather than
//     "produced a machine that looks about right".
//
// The second property is the one that matters most. It turns "did we capture
// everything" from a question answered by reading code into one answered by
// running the machine twice.
//
// # Writing a capture test
//
// Read this before adding a Device. Every one of these was learned by getting
// it wrong first, and each cost real time.
//
// Assert on the CAPTURE, not on behaviour. Drive the device so every captured
// field changes, restore, re-capture, and compare the two blobs. Behavioural
// assertions only observe fields that reach behaviour: 11 of the AY's 19
// fields did not, and its register-select latch cannot reach the audio at all,
// so no amount of comparing samples would ever have covered it.
//
// A mutation score is bounded above by what the fixture moves. Deleting a
// field restore proves nothing if the fixture never made that field differ
// between capture and restore, because the deleted line had nothing to do. So
// a score is only meaningful reported next to the fields the fixture actually
// moves: 20/20 over a fixture that moves twelve fields is a WEAKER claim than
// 12/12 over one that moves all twenty, and the bare number cannot tell them
// apart. Write a guard test that asserts the drive sequence changes every
// captured field, and the score becomes trustworthy.
//
// Watch the parity. An even number of toggles returns a flag to where it
// started, so an enable flipped twice, or a double-buffer swapped four times,
// hides a lost restore completely. Prefer odd counts, and make the guard fail
// if anyone retunes the schedule into an even parity later.
//
// Watch for convergence. A field that moves and then comes back is as
// invisible as one that never moved: a press-and-release keypress re-converges
// the state before the comparison, and so does any register the guest resets
// on its way past.
//
// Some fields are mutually exclusive and need separate fixtures. The AY's
// envPrimed is set by a shape write and cleared by the first envelope tick,
// while envHolding needs a completed envelope, so one drive sequence cannot
// move both and each needs its own round trip.
//
// Finally: when a differential check does not fail where you expect, suspect
// the harness before the code. A build error and an already-red baseline both
// exit non-zero exactly like a real failure, and both score a mutation as
// "caught" when it proved nothing. Assert the baseline is green, and match
// failures against the specific assertion you expect rather than an exit code.
package machinestate

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// Device is one addressable piece of machine state.
//
// SaveState returns a buffer the registry takes ownership of; the device must
// not retain or mutate it afterwards. LoadState receives a buffer the device
// must not retain.
//
// StateID must be stable across builds. It appears in stored state and in the
// error naming a device that failed to restore.
type Device interface {
	StateID() string
	SaveState() []byte
	LoadState([]byte) error
}

// State is a complete machine state: every registered device's bytes, ordered
// by name so the encoding is canonical.
type State struct {
	entries []entry
}

type entry struct {
	id   string
	blob []byte
}

// Bytes is the canonical encoding, and two states compare equal exactly when
// they describe the same machine.
//
// The length prefixes are what make that true: without them, a device whose
// state ends in the bytes of the next device's name would encode ambiguously.
func (s State) Bytes() []byte {
	n := 0
	for _, e := range s.entries {
		n += 8 + len(e.id) + len(e.blob)
	}
	out := make([]byte, 0, n)
	var hdr [4]byte
	for _, e := range s.entries {
		binary.BigEndian.PutUint32(hdr[:], uint32(len(e.id)))
		out = append(out, hdr[:]...)
		out = append(out, e.id...)
		binary.BigEndian.PutUint32(hdr[:], uint32(len(e.blob)))
		out = append(out, hdr[:]...)
		out = append(out, e.blob...)
	}
	return out
}

// Size is the total bytes held, which is what bounds a rewind ring.
//
// It counts the framing as well as the payload, matching what Bytes actually
// emits. Counting only the blobs would undercount every capture by eight bytes
// plus the device name per device, so a ring sized from this against a memory
// budget would quietly exceed it.
func (s State) Size() int {
	n := 0
	for _, e := range s.entries {
		n += 8 + len(e.id) + len(e.blob)
	}
	return n
}

// Devices lists the device names in this state, for diagnostics.
func (s State) Devices() []string {
	out := make([]string, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.id
	}
	return out
}

// Registry is the set of devices making up one machine.
type Registry struct {
	devices map[string]Device
}

// New builds an empty registry.
func New() *Registry { return &Registry{devices: map[string]Device{}} }

// Register adds devices to the machine.
//
// A duplicate identifier panics rather than returning an error, because it is
// a wiring mistake rather than a runtime condition: two devices sharing a name
// would overwrite each other's state, and the rewind would restore whichever
// happened to be applied last. Failing at wiring time makes that impossible
// to ship.
func (r *Registry) Register(ds ...Device) {
	for _, d := range ds {
		id := d.StateID()
		if _, dup := r.devices[id]; dup {
			panic("machinestate: duplicate device identifier " + id)
		}
		r.devices[id] = d
	}
}

// Len is how many devices the machine has.
func (r *Registry) Len() int { return len(r.devices) }

// Capture takes a complete state.
//
// The blob each device returns is copied, so a device that hands back a view
// of its live memory (a RAM bank, most obviously) cannot rewrite its own
// history by carrying on running.
func (r *Registry) Capture() State {
	ids := make([]string, 0, len(r.devices))
	for id := range r.devices {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	st := State{entries: make([]entry, 0, len(ids))}
	for _, id := range ids {
		src := r.devices[id].SaveState()
		blob := make([]byte, len(src))
		copy(blob, src)
		st.entries = append(st.entries, entry{id: id, blob: blob})
	}
	return st
}

// Restore returns every device to the captured state, all or nothing.
//
// A half-rewound machine is the worst outcome available here: it runs, and the
// caller believes the rewind worked. Two things stand between us and one.
//
// The device set is checked in full before anything is applied, which catches
// a state describing a different machine. That check cannot tell whether each
// blob will actually decode, though, so a device rejecting its state part way
// through the loop would leave every earlier device already restored. So the
// current state is captured first and rolled back on failure.
//
// The rollback can itself fail, and that case is reported rather than hidden:
// at that point the machine is genuinely inconsistent and the caller needs to
// know it cannot keep running.
func (r *Registry) Restore(s State) error {
	if err := r.check(s); err != nil {
		return err
	}

	rollback := r.Capture()
	for i, e := range s.entries {
		if err := r.devices[e.id].LoadState(e.blob); err != nil {
			// i+1, not i: the device that FAILED is unwound too. A device is
			// entitled to mutate itself before discovering the state is bad,
			// and one that does would otherwise keep its half-applied bytes
			// while every device around it was restored. Our own devices all
			// decode before applying and cannot do this; the registry does not
			// get to assume that of every device that will ever implement the
			// interface.
			if rbErr := r.rollback(rollback, i+1); rbErr != nil {
				return fmt.Errorf("machinestate: device %q refused its state (%w), "+
					"AND the machine could not be rolled back: %v", e.id, err, rbErr)
			}
			return fmt.Errorf("machinestate: device %q refused its state: %w", e.id, err)
		}
	}
	return nil
}

// rollback restores the first n devices of a previously captured state, which
// is exactly the set Restore had already applied when it failed.
func (r *Registry) rollback(prev State, n int) error {
	for i := 0; i < n && i < len(prev.entries); i++ {
		e := prev.entries[i]
		if err := r.devices[e.id].LoadState(e.blob); err != nil {
			return fmt.Errorf("device %q: %w", e.id, err)
		}
	}
	return nil
}

// check verifies the state describes exactly this machine.
func (r *Registry) check(s State) error {
	seen := make(map[string]bool, len(s.entries))
	for _, e := range s.entries {
		if _, ok := r.devices[e.id]; !ok {
			return fmt.Errorf("machinestate: state carries device %q, which this machine does not have", e.id)
		}
		seen[e.id] = true
	}
	missing := make([]string, 0)
	for id := range r.devices {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("machinestate: state has nothing for registered device(s) %v; "+
			"restoring would leave them holding present-day values", missing)
	}
	return nil
}
