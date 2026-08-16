package next

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture for the NextReg state pkg/next's own wiring owns, as
// opposed to the state it forwards into a subsystem that captures it.
//
// Everything here was unreachable from any capture until it had a Device,
// because it lived inside a Wire* function's closure. The registers a
// dispatcher stores are recoverable from pkg/next/nextregs; these are not,
// which is exactly why they are easy to forget: the machine looks fully
// captured right up to the first rewind that lands mid-sequence.

// clipWindowsState is the wire form of the four clip windows. Every field is
// exported because gob only encodes exported fields, and every field of the
// running windows is present.
//
// The per-window `def` array is deliberately absent: it is a construction
// constant, identical in every instance WireClipWindows builds, and never
// written after. Capturing it would add a field no fixture can move, which
// makes a mutation audit report a kill it never earned.
type clipWindowsState struct {
	Layer2Coord [4]byte
	Layer2Idx   byte

	SpriteCoord [4]byte
	SpriteIdx   byte

	ULACoord [4]byte
	ULAIdx   byte

	TilemapCoord [4]byte
	TilemapIdx   byte
}

// StateID identifies the clip windows in a captured machine state.
func (c *ClipWindows) StateID() string { return "next.clipwindows" }

// SaveState captures all four windows and their write indices.
func (c *ClipWindows) SaveState() []byte {
	s := clipWindowsState{
		Layer2Coord:  c.l2.coord,
		Layer2Idx:    c.l2.idx,
		SpriteCoord:  c.spr.coord,
		SpriteIdx:    c.spr.idx,
		ULACoord:     c.ula.coord,
		ULAIdx:       c.ula.idx,
		TilemapCoord: c.tm.coord,
		TilemapIdx:   c.tm.idx,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size values cannot fail in practice, and the capture
		// path skips a failed capture rather than stopping the machine. A nil
		// blob is rejected by LoadState, so a bad capture surfaces as a failed
		// restore instead of a crash mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// It does NOT push the tilemap or sprite coordinates back down into those
// layers: each of them captures the coordinates it was given under its own
// name, from the same instant, so the registry restores them itself. Pushing
// here would write the same values a second time and make the restore order
// matter where it currently does not.
func (c *ClipWindows) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("next: empty clip-window state (the capture failed)")
	}
	var s clipWindowsState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("next: decoding clip-window state: %w", err)
	}
	c.l2.coord = s.Layer2Coord
	c.l2.idx = s.Layer2Idx
	c.spr.coord = s.SpriteCoord
	c.spr.idx = s.SpriteIdx
	c.ula.coord = s.ULACoord
	c.ula.idx = s.ULAIdx
	c.tm.coord = s.TilemapCoord
	c.tm.idx = s.TilemapIdx
	return nil
}

// resetState is the wire form of the NR$02 reset control. Every field is
// exported because gob only encodes exported fields, and every field of the
// running control is present.
type resetState struct {
	ResetType byte
	NMISource byte
}

// StateID identifies the NR$02 reset control in a captured machine state.
func (r *ResetControl) StateID() string { return "next.reset" }

// SaveState captures the reset-type history and the NMI-source latch.
//
// ResetType is the full three bits, not the two a guest can read: the top bit
// is what the next soft reset shifts down, so a capture of the visible pair
// would restore a machine that answers the next NR$02 read differently.
func (r *ResetControl) SaveState() []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(resetState{
		ResetType: r.resetType,
		NMISource: r.nmiSource,
	}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
func (r *ResetControl) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("next: empty reset state (the capture failed)")
	}
	var s resetState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("next: decoding reset state: %w", err)
	}
	r.resetType = s.ResetType
	r.nmiSource = s.NMISource
	return nil
}
