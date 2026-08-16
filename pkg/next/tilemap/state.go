package tilemap

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The tilemap holds no counter, latch or transfer cursor: every field is the
// value a NextReg write last put there, and the renderer derives each scanline
// from those alone. So the capture is the register mirror, and completeness is
// the only thing that can go wrong — leave the scroll, one clip edge or the
// default attribute out and the layer still draws a picture, just not the one
// the machine was rewound to.
//
// What is NOT here, and why:
//
//   - mem is the RAM bus (pkg/memory), which captures itself under its own name
//     in the machinestate registry. Every tile and every map entry lives there,
//     not here; copying the interface value into this blob would restore a
//     reference rather than a state, and would take a second, stale copy of two
//     16 KB banks into every rewind point.
//   - The NR$1B clip-window write index — which of x1/x2/y1/y2 the next write
//     lands in — is not the layer's. It lives in the clipWindow helper inside
//     pkg/next/wire.go, which pushes finished coordinate sets down through
//     SetClip; the layer never sees a partial one. That helper is pkg/next's
//     ClipWindows, which captures the index — and the other three layers'
//     NR$18/$19/$1A state — under "next.clipwindows".
//   - NR$4C, the tilemap transparency index, is the compositor's: it decides
//     which rendered index reads as see-through, and pkg/next/compositor holds
//     it (tilemapTrans). The layer emits palette indices and has no opinion.
//
// The base registers are stored as the CPU wrote them, bit 6 included. The FPGA
// drops that bit (zxnext.vhd:5468-5473 splits the write into bit 7 and bits
// 5:0), and the renderer ignores it to match — but SaveState's job is to hand
// back the field, not to reinterpret it, so the round trip is exact.
//
// The tilemap has no lock; like every other access to it (NextReg writes,
// RenderScanline), SaveState and LoadState belong on the emulation goroutine.

// tilemapState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running layer is present:
// adding a field to Tilemap without adding it here is the failure this
// package's state tests are built to catch.
type tilemapState struct {
	Enabled     bool
	Control     byte
	DefaultAttr byte
	MapBase     byte
	TilesBase   byte

	ScrollX int
	ScrollY int

	ClipX1 byte
	ClipX2 byte
	ClipY1 byte
	ClipY2 byte
}

// StateID identifies the tilemap in a captured machine state.
func (t *Tilemap) StateID() string { return "next.tilemap" }

// SaveState captures the complete layer state.
func (t *Tilemap) SaveState() []byte {
	s := tilemapState{
		Enabled:     t.enabled,
		Control:     t.control,
		DefaultAttr: t.defaultAttr,
		MapBase:     t.mapBase,
		TilesBase:   t.tilesBase,
		ScrollX:     t.scrollX,
		ScrollY:     t.scrollY,
		ClipX1:      t.clipX1,
		ClipX2:      t.clipX2,
		ClipY1:      t.clipY1,
		ClipY2:      t.clipY2,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values cannot fail for any reason the
		// caller could act on, and returning an error here would push a dead
		// branch into every caller.
		panic("tilemap: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the layer untouched rather than half-rewound: a layer holding a new
// map base against an old tile-defs base draws a picture that looks like a
// corrupt guest rather than a failed restore.
func (t *Tilemap) LoadState(b []byte) error {
	var s tilemapState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("tilemap: decoding state: %w", err)
	}

	t.enabled = s.Enabled
	t.control = s.Control
	t.defaultAttr = s.DefaultAttr
	t.mapBase = s.MapBase
	t.tilesBase = s.TilesBase
	t.scrollX = s.ScrollX
	t.scrollY = s.ScrollY
	t.clipX1 = s.ClipX1
	t.clipX2 = s.ClipX2
	t.clipY1 = s.ClipY1
	t.clipY2 = s.ClipY2
	return nil
}
