package audiodac

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The latched sample value is the small part. What actually determines the next
// frame is the level carried over from the previous frame plus the writes that
// have been timed but not yet mixed: Record only queues an event, and nothing
// is folded into the level until GenerateFrame runs. A capture taken mid-frame
// that stopped at the level would restore a DAC missing every write since the
// last frame boundary — it would run, at the right volume, playing a different
// drum.
//
// The two enable flags are captured for the same reason. They are not a
// guest-visible register, but they gate whether an OUT reaches the device at
// all (see Handles), so a machine rewound across a SpecDrum or Covox toggle
// would otherwise carry today's wiring back into yesterday's frame. The GUI
// reads the flags back off the device when it builds its menu, so a restore
// that changes them stays consistent with what the user is shown.

// dacEvent is the wire form of one timed write. The running type's fields are
// unexported and gob only encodes exported fields, so the event buffer needs a
// parallel form rather than being encoded directly.
type dacEvent struct {
	TStateOffset int
	Level        byte
}

// dacState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running device is present: adding a
// field to DAC without adding it here is the failure this package's tests are
// built to catch.
type dacState struct {
	SpecDrum   bool
	Covox      bool
	StartLevel byte
	Events     []dacEvent
}

// StateID identifies the DAC in a captured machine state.
func (d *DAC) StateID() string { return "audiodac" }

// SaveState captures the complete device state.
func (d *DAC) SaveState() []byte {
	s := dacState{
		SpecDrum:   d.specDrum,
		Covox:      d.covox,
		StartLevel: d.startLevel,
		Events:     make([]dacEvent, len(d.events)),
	}
	for i, e := range d.events {
		s.Events[i] = dacEvent{TStateOffset: e.tstateOffset, Level: e.level}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values cannot fail for any reason
		// the caller could act on, and returning an error here would push a
		// dead branch into every caller.
		panic("audiodac: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (d *DAC) LoadState(b []byte) error {
	var s dacState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("audiodac: decoding state: %w", err)
	}

	d.specDrum = s.SpecDrum
	d.covox = s.Covox
	d.startLevel = s.StartLevel
	d.events = d.events[:0]
	for _, e := range s.Events {
		d.events = append(d.events, event{tstateOffset: e.TStateOffset, level: e.Level})
	}
	return nil
}
