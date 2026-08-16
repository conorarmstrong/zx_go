package copper

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The 1024-instruction list is the bulk of the bytes but not the part that
// decides what happens next. That comes from the small state around it: where
// the program counter is parked inside the list, whether the copper is running
// and in which start mode, the raster line the last step was taken at, the
// write cursor, and the NR$60 hi/lo byte-pairing phase.
//
// Two of those deserve naming, because they are invisible to the guest and to
// every snapshot format:
//
//   - The pairing phase. NR$60 delivers a 16-bit instruction as two consecutive
//     register writes: the first is latched in hi, the second commits the pair.
//     Restore between them without the phase and the guest's next byte is taken
//     as a high half when it was meant as a low one — every instruction after
//     it is off by one, so a real "WAIT y; MOVE r,v" list resumes as garbage
//     MOVEs that clobber the whole NextReg config. Not resetting that phase on
//     a cursor set was a real bug here (see SetWritePtrLow); losing it across a
//     rewind is the same failure one step along.
//   - lastScanline. It is one half of a comparison: a StartOnVBL restart fires
//     when the line being stepped is below the line last stepped. Restore
//     without it and the first step after the rewind either invents a restart
//     that did not happen or misses one that did, and the list runs a frame out
//     of phase with the raster it is supposed to be painting.
//
// What is NOT here, and why:
//
//   - regs is the RegWriter, wiring to the NextReg dispatcher installed by
//     SetRegWriter at machine-build time (WireCopper, pkg/next/wire.go).
//     Capturing it would restore a function table, not a state, and a rewind
//     that silently reattached or replaced the dispatcher would change where
//     every subsequent MOVE lands. It survives a restore untouched, which is
//     what the caller that installed it expects.
//
// There is no horizontal position to capture. The copper's 8-pixel WAIT column
// lives in the instruction word, and the raster position it is compared against
// arrives as an argument to Step from the video timing rather than being held
// here (see WaitHThreshold), so the only raster state the copper itself owns is
// lastScanline. Storing an hcount would be inventing a field the device does
// not have.
//
// The copper has no lock of its own; like every other access to it (NextReg
// writes, Step), SaveState and LoadState belong on the emulation goroutine.

// copperState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running Copper is present:
// adding a field to Copper without adding it here is the failure this package's
// tests are built to catch.
type copperState struct {
	Program [MaxInstructions]uint16

	WritePtr uint16
	Hi       byte
	HiSet    bool

	Mode    byte
	Pc      uint16
	Stopped bool

	LastScanline uint16
}

// StateID identifies the copper in a captured machine state.
func (c *Copper) StateID() string { return "next.copper" }

// SaveState captures the complete copper state.
func (c *Copper) SaveState() []byte {
	s := copperState{
		Program:      c.program,
		WritePtr:     c.writePtr,
		Hi:           c.hi,
		HiSet:        c.hiSet,
		Mode:         byte(c.mode),
		Pc:           c.pc,
		Stopped:      c.stopped,
		LastScanline: c.lastScanline,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a fixed-size array of fixed-size values cannot fail for any
		// reason the caller could act on, and returning an error here would
		// push a dead branch into every caller. A nil blob is rejected by
		// LoadState, so even an impossible failure surfaces as a failed restore
		// rather than a silently empty copper list.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the copper untouched rather than half-rewound — a copper with its list
// restored but its pairing phase left in the present is exactly the corruption
// this file exists to prevent.
func (c *Copper) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("copper: empty state (the capture failed)")
	}
	var s copperState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("copper: decoding state: %w", err)
	}

	c.program = s.Program
	c.writePtr = s.WritePtr
	c.hi = s.Hi
	c.hiSet = s.HiSet
	c.mode = StartMode(s.Mode)
	c.pc = s.Pc
	c.stopped = s.Stopped
	c.lastScanline = s.LastScanline
	return nil
}
