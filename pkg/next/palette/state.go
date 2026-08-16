package palette

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The eight tables are the bulk of the bytes but not the part that decides what
// the next guest write does. That comes from the small state around them: the
// write cursor NR$40 installed, which of the eight tables NR$43 pointed the
// write path at, whether NR$43 bit 7 has auto-increment disabled, which table
// each layer is rendering through, and — the one that bites hardest — the NR$44
// two-byte pairing phase.
//
// NR$44 delivers a 9-bit entry as two consecutive register writes: the first is
// latched, the second commits both. Capture between them and restore without
// the phase, and the guest's next write is read as a first byte when it was
// meant as a second. Every pair after it is off by one, so entries take the
// wrong colour and the wrong priority from that point on, and the damage is
// silent — the machine keeps running, just not as the machine that was
// rewound. A Copper hi/lo byte-pairing phase that was not reset was a real
// crash in this emulator; this is the same failure one register along.
//
// What is NOT here, and why:
//
//   - writeObserver is the raster journal's callback, installed from outside
//     and owned by the installer. It is wiring to another subsystem, not
//     machine state: capturing it would restore a function pointer, and a
//     rewind that silently attached or detached the journal would change what
//     the NEXT frame journals. It survives a restore untouched, which is what
//     the caller expects of something it installed.

// bankState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running Bank is present: adding a
// field to Bank without adding it here is the failure this package's tests are
// built to catch.
//
// Entries and Priorities are flattened out of the [8]*Palette so the blob holds
// values rather than pointers, and so a decoded state can be written into the
// Palette structs the render path already holds pointers to.
type bankState struct {
	Entries    [8][256]uint16
	Priorities [8][256]byte

	Selected byte
	Index    byte

	ActiveULA     byte
	ActiveLayer2  byte
	ActiveSprites byte
	ActiveTilemap byte

	Pending9 byte
	Have9    bool

	AutoIncDisable bool
}

// StateID identifies the palette bank in a captured machine state.
func (b *Bank) StateID() string { return "next.palette" }

// SaveState captures the complete bank state.
func (b *Bank) SaveState() []byte {
	s := bankState{
		Selected: b.selected,
		Index:    b.index,

		ActiveULA:     b.activeULA,
		ActiveLayer2:  b.activeLayer2,
		ActiveSprites: b.activeSprites,
		ActiveTilemap: b.activeTilemap,

		Pending9: b.pending9,
		Have9:    b.have9,

		AutoIncDisable: b.autoIncDisable,
	}
	for i, p := range b.palettes {
		s.Entries[i] = p.entries
		s.Priorities[i] = p.priority
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size arrays of fixed-size values cannot fail for any
		// reason the caller could act on, and returning an error here would
		// push a dead branch into every caller. A nil blob is rejected by
		// LoadState, so even an impossible failure surfaces as a failed restore
		// rather than a silently empty palette.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the bank untouched rather than half-rewound — a bank with its tables
// restored but its pairing phase left in the present is exactly the corruption
// this file exists to prevent.
//
// The tables are written into the existing Palette structs rather than
// replaced, because the compositor and the layer code hold the pointers
// Palette() and PaletteForLayer() handed out.
func (b *Bank) LoadState(buf []byte) error {
	if len(buf) == 0 {
		return fmt.Errorf("palette: empty state (the capture failed)")
	}
	var s bankState
	if err := gob.NewDecoder(bytes.NewReader(buf)).Decode(&s); err != nil {
		return fmt.Errorf("palette: decoding state: %w", err)
	}

	for i, p := range b.palettes {
		p.entries = s.Entries[i]
		p.priority = s.Priorities[i]
	}

	b.selected = s.Selected
	b.index = s.Index

	b.activeULA = s.ActiveULA
	b.activeLayer2 = s.ActiveLayer2
	b.activeSprites = s.ActiveSprites
	b.activeTilemap = s.ActiveTilemap

	b.pending9 = s.Pending9
	b.have9 = s.Have9

	b.autoIncDisable = s.AutoIncDisable
	return nil
}
