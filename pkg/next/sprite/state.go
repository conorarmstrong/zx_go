package sprite

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The 128 attribute records and the 16 KB of pattern RAM are the obvious part,
// and the part nobody forgets. What makes a capture resumable rather than
// merely plausible is the upload machinery around them. A guest re-uploads its
// whole sprite bank every frame through two byte streams — port $57 for
// attributes, port $5B for patterns — and neither stream is transactional:
// there is no "end of record" write, only a cursor that counts to four (or to
// five, when byte 3 says a fifth byte follows) and then auto-advances to the
// next sprite. So a capture taken between two bytes of one record has to carry
// the sprite that record belongs to, how far into it the stream has got, and
// where in pattern RAM the other stream is writing. Restore those at rest and
// the machine resumes by putting the rest of the record into sprite 0 at byte
// 0, and the rest of the pattern into the wrong half of the wrong slot.
//
// The two sticky $303B status latches are captured for the same reason: nothing
// clears collision or max-sprites-per-line except a read of that port, so a
// game polling once a frame sees a flag whose whole meaning is "since you last
// looked". A restore that dropped it would report no collision for a frame that
// had one.
//
// What is NOT here, and why:
//
//   - lineCovered, the per-pixel coverage map behind collision detection. It is
//     scratch for one scanline: RenderScanline re-sizes it and zeroes every
//     entry before the sprite walk starts, so nothing in it can outlive the
//     call. The latch it feeds (collided) IS captured, and that is the part
//     that persists.
//   - The anchor state that positions relative sprites. It is a local of
//     RenderScanline, rebuilt from the attribute records by every line's walk
//     over sprites 0..127, so there is no anchor to carry between lines.
//   - The NR$19 clip-window write index — which of x1/x2/y1/y2 the next write
//     lands in. It is not the engine's: it lives in the clipWindow helper in
//     pkg/next/wire.go, which pushes finished coordinate sets down through
//     SetClip, so the engine never sees a partial one. That helper is
//     pkg/next's ClipWindows, which captures the index — and the other three
//     layers' NR$18/$1A/$1B state — under "next.clipwindows".
//
// attrExtended is captured and is currently redundant: WriteAttr can only have
// it set while the byte cursor is parked at 4, and a write there advances to
// the next sprite whether the record was four bytes or five, so a restore that
// dropped it would be corrected by the cursor on the next byte. Keeping the two
// in step is WriteAttr's invariant to hold, not the capture's to assume, so the
// field is saved rather than re-derived (see TestAttrExtendedIsExactlyACursor-
// ParkedAtFour, which fails the day that stops being true).
//
// clocksPerLine is captured even though it is wiring the machine sets up once,
// because it is what decides whether the walk finishes, and a state carrying
// the sprites but not their time budget would restore a frame that draws a
// different number of them.
//
// The engine has no lock; like every other access to it (NextReg writes, port
// uploads, RenderScanline), SaveState and LoadState belong on the emulation
// goroutine.

// spriteState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running engine is present
// bar the scratch named above: adding a field to Engine without adding it here
// is the failure this package's state tests are built to catch.
//
// Pattern is a slice rather than the array it comes from because gob encodes a
// []byte as one byte string and a [N]byte element by element.
type spriteState struct {
	Attrs   [MaxSprites]Attr
	Pattern []byte

	SelSprite    byte
	SelPattAddr  uint16
	AttrCursor   int
	AttrExtended bool

	Enabled   bool
	ZeroOnTop bool

	Collided bool
	Overtime bool

	ClocksPerLine int

	ClipX1     byte
	ClipX2     byte
	ClipY1     byte
	ClipY2     byte
	ClipSet    bool
	OverBorder bool
	BorderClip bool
}

// StateID identifies the sprite engine in a captured machine state.
func (e *Engine) StateID() string { return "next.sprite" }

// SaveState captures the complete engine state.
//
// The state struct references the live pattern RAM rather than copying it: gob
// serialises it into the returned blob on the spot, so the 16 KB is copied once
// instead of twice.
func (e *Engine) SaveState() []byte {
	s := spriteState{
		Attrs:   e.attrs,
		Pattern: e.pattern[:],

		SelSprite:    e.selSprite,
		SelPattAddr:  e.selPattAddr,
		AttrCursor:   e.attrCursor,
		AttrExtended: e.attrExtended,

		Enabled:   e.enabled,
		ZeroOnTop: e.zeroOnTop,

		Collided: e.collided,
		Overtime: e.overtime,

		ClocksPerLine: e.clocksPerLine,

		ClipX1:     e.clipX1,
		ClipX2:     e.clipX2,
		ClipY1:     e.clipY1,
		ClipY2:     e.clipY2,
		ClipSet:    e.clipSet,
		OverBorder: e.overBorder,
		BorderClip: e.borderClip,
	}

	var buf bytes.Buffer
	buf.Grow(PatternRAMSize + 4096)
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and a byte slice cannot fail
		// for any reason the caller could act on, and returning an error here
		// would push a dead branch into every caller.
		panic("sprite: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded and checked before any of it is applied, so a
// malformed state leaves the engine untouched rather than half-rewound: an
// engine holding a restored attribute bank over present-day patterns draws
// something that looks like a corrupt guest rather than a failed restore.
func (e *Engine) LoadState(b []byte) error {
	var s spriteState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("sprite: decoding state: %w", err)
	}

	// The cursors index their storage directly on the next upload byte, so an
	// impossible one has to be refused here: accepted, it would surface as a
	// panic (or as writes silently going nowhere) some frames later with
	// nothing left pointing at the cause. Every value below is one the hardware
	// paths cannot produce — SelectSlot masks the pattern slot to 6 bits and
	// the sprite to 7, and the attribute cursor never passes 4 — so a blob
	// carrying one did not come from this engine.
	if len(s.Pattern) != PatternRAMSize {
		return fmt.Errorf("sprite: state carries %d bytes of pattern RAM, want %d",
			len(s.Pattern), PatternRAMSize)
	}
	if int(s.SelPattAddr) >= PatternRAMSize {
		return fmt.Errorf("sprite: pattern write cursor %d is outside the %d-byte pattern RAM",
			s.SelPattAddr, PatternRAMSize)
	}
	if int(s.SelSprite) >= MaxSprites {
		return fmt.Errorf("sprite: selected sprite %d is outside the %d slots", s.SelSprite, MaxSprites)
	}
	if s.AttrCursor < 0 || s.AttrCursor > 4 {
		return fmt.Errorf("sprite: attribute byte cursor %d is outside a 5-byte record", s.AttrCursor)
	}

	e.attrs = s.Attrs
	copy(e.pattern[:], s.Pattern)

	e.selSprite = s.SelSprite
	e.selPattAddr = s.SelPattAddr
	e.attrCursor = s.AttrCursor
	e.attrExtended = s.AttrExtended

	e.enabled = s.Enabled
	e.zeroOnTop = s.ZeroOnTop

	e.collided = s.Collided
	e.overtime = s.Overtime

	e.clocksPerLine = s.ClocksPerLine

	e.clipX1 = s.ClipX1
	e.clipX2 = s.ClipX2
	e.clipY1 = s.ClipY1
	e.clipY2 = s.ClipY2
	e.clipSet = s.ClipSet
	e.overBorder = s.OverBorder
	e.borderClip = s.BorderClip
	return nil
}
