package if1

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The cartridge bytes are the small part. A microdrive is a tape loop with a
// single head, and what the next MDR read hands back is decided by where that
// head sits, how far through the current transfer window the controller has
// got, which byte was last latched, how far down the GAP and SYNC countdowns
// the status port has walked, and how much of a 12-byte preamble the block
// under the head has seen. None of that is readable by the guest and none of
// it appears in any snapshot format. A capture that stopped at the media would
// restore a drive that spins but does not resume: the same tape, a different
// position on it, and the block the IF1 ROM was half-way through never
// arrives.
//
// The shadow ROM image is deliberately not captured. It is firmware, fixed at
// wiring time and unwritable by the guest, so it is identical either side of
// any capture of the same machine; storing 8 KB per rewind frame for bytes
// that cannot differ would be pure cost. The cartridge contents ARE captured,
// because the guest writes to them (FORMAT and SAVE both do), and a rewind
// that left those writes in place would give the guest a tape it has not
// written yet — and would restore Modified = false over a genuinely dirty
// cartridge, which is how a state layer silently loses a user's data.

// if1State is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running interface is present: adding
// a field to IF1, ULA or Drive without adding it here is the failure this
// package's tests are built to catch.
type if1State struct {
	Active       bool
	CommsClkHigh bool
	Drives       [NumDrives]driveState
}

// driveState is one microdrive bay: the drive's own runtime state, plus the
// cartridge sitting in it.
type driveState struct {
	Inserted bool
	Modified bool
	MotorOn  bool

	HeadPos     int
	Transferred int
	MaxBytes    int
	Gap         byte
	Sync        byte
	LastByte    byte
	Pream       [512]byte

	CartPresent      bool
	CartBlocks       int
	CartWriteProtect bool
	CartData         []byte
}

// StateID identifies the interface in a captured machine state.
func (i *IF1) StateID() string { return "if1" }

// SaveState captures the complete interface state.
func (i *IF1) SaveState() []byte {
	s := if1State{
		Active:       i.active,
		CommsClkHigh: i.ULA.commsClkHigh,
	}
	for n := range s.Drives {
		d := &i.ULA.Bus.drives[n]
		ds := &s.Drives[n]
		ds.Inserted = d.Inserted
		ds.Modified = d.Modified
		ds.MotorOn = d.MotorOn
		ds.HeadPos = d.headPos
		ds.Transferred = d.transferred
		ds.MaxBytes = d.maxBytes
		ds.Gap = d.gap
		ds.Sync = d.sync
		ds.LastByte = d.lastByte
		ds.Pream = d.pream
		if d.Cartridge != nil {
			ds.CartPresent = true
			ds.CartBlocks = d.Cartridge.Length()
			ds.CartWriteProtect = d.Cartridge.WriteProtect()
			ds.CartData = d.Cartridge.RawBytes() // already a copy
		}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and byte slices cannot fail
		// for any reason the caller could act on, and returning an error here
		// would push a dead branch into every caller.
		panic("if1: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (i *IF1) LoadState(b []byte) error {
	var s if1State
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("if1: decoding state: %w", err)
	}

	// Every bay is validated before any of them is applied. A drive left
	// holding half a restored cartridge would keep running and read the wrong
	// tape, which is the failure hardest to notice: the machine does not stop,
	// it just loads the wrong bytes.
	for n := range s.Drives {
		ds := &s.Drives[n]
		if !ds.CartPresent {
			continue
		}
		if ds.CartBlocks < 1 || ds.CartBlocks > microdrive.MaxBlocks {
			return fmt.Errorf("if1: decoding state: drive %d cartridge length %d blocks out of range",
				n, ds.CartBlocks)
		}
		if want := ds.CartBlocks * microdrive.BlockLen; len(ds.CartData) != want {
			return fmt.Errorf("if1: decoding state: drive %d carries %d cartridge bytes, want %d",
				n, len(ds.CartData), want)
		}
	}

	i.active = s.Active
	i.ULA.commsClkHigh = s.CommsClkHigh
	for n := range s.Drives {
		i.ULA.Bus.drives[n].loadState(&s.Drives[n])
	}
	return nil
}

// loadState returns one bay to its captured contents and position.
//
// The bays are restored to the configuration the capture found, which means a
// cartridge inserted after the capture is removed and one ejected after it is
// put back. That is the same rule the rest of the rewind follows — the machine
// becomes the machine that was captured — and nothing is invented: the state
// carries every byte of the cartridge it restores.
func (d *Drive) loadState(s *driveState) {
	d.Inserted = s.Inserted
	d.Modified = s.Modified
	d.MotorOn = s.MotorOn
	d.headPos = s.HeadPos
	d.transferred = s.Transferred
	d.maxBytes = s.MaxBytes
	d.gap = s.Gap
	d.sync = s.Sync
	d.lastByte = s.LastByte
	d.pream = s.Pream

	if !s.CartPresent {
		d.Cartridge = nil
		return
	}
	// An existing cartridge is written through rather than replaced, so the
	// GUI and the save-to-file path keep pointing at the object they were
	// given when it was inserted.
	c := d.Cartridge
	if c == nil {
		c = microdrive.New(s.CartBlocks)
		d.Cartridge = c
	}
	if c.Length() != s.CartBlocks {
		c.SetLength(s.CartBlocks)
	}
	for off, b := range s.CartData {
		c.SetDataAt(off, b)
	}
	c.SetWriteProtect(s.CartWriteProtect)
}
