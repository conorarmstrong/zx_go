package layer2

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// Layer 2's control state is small, and all of it is guest-writable, but none
// of it is derivable from the framebuffer it draws: the scroll pair, the
// resolution and the palette offset decide WHERE in RAM each displayed pixel
// comes from and what index it becomes, and the active bank decides which of
// the two buffers the guest is showing. A capture that restored the RAM and
// left these behind would restore the picture's contents and lose the picture:
// the same bytes, read at a different offset, in the wrong mode.
//
// What is NOT here, and why:
//
//   - mem is the memory bus. pkg/memory captures itself (the machinestate
//     registry names it separately), so copying the pointer into this blob
//     would restore a reference, not a state. The framebuffer PIXELS live in
//     those RAM banks and are captured there, once, rather than twice.
//   - scrollObserver is the raster journal's hook, installed from outside
//     (cmd/zx_go/next.go). It is the observer's property, not the machine's,
//     and a rewind must not silently reattach or detach one. For the same
//     reason LoadState writes the scroll fields DIRECTLY instead of going
//     through SetScrollX/SetScrollY: those report a change to the journal, and
//     a rewind is not a guest write. Feeding one into a journal that is itself
//     being rewound would replay the restore as a mid-frame scroll effect.
//
// Two things a reader may expect to find here and will not, because this
// package does not own them:
//
//   - The Layer 2 write-enable, read-enable, segment and offset from the
//     legacy port $123B are pkg/memory's (m.l2WrEn and friends), and its state
//     blob already carries them. Capturing them again here would give two
//     devices the same field and let the restore order decide which value
//     survives.
//   - The NR$18 clip window and its 2-bit auto-incrementing write index live
//     in the clipWindow value that pkg/next's WireClipWindows closes over.
//     Nothing in this package can reach it, and it is not yet a
//     machinestate.Device of its own — see the note in state_test.go.
//
// Layer 2 has no lock; like every other access to it (register writes,
// RenderScanline), SaveState and LoadState belong on the emulation goroutine.

// layer2State is the wire form. Every field is exported because gob only
// encodes exported fields, and every capturable field of the running layer is
// present: adding a field to Layer2 without adding it here is the failure this
// package's tests are built to catch.
type layer2State struct {
	ActiveBank    byte
	ShadowBank    byte
	Enabled       bool
	Resolution    byte
	PaletteOffset byte
	ScrollX       uint16
	ScrollY       byte
}

// StateID identifies Layer 2 in a captured machine state.
func (l *Layer2) StateID() string { return "next.layer2" }

// SaveState captures the complete Layer 2 control state.
func (l *Layer2) SaveState() []byte {
	s := layer2State{
		ActiveBank:    l.activeBank,
		ShadowBank:    l.shadowBank,
		Enabled:       l.enabled,
		Resolution:    l.resolution,
		PaletteOffset: l.paletteOffset,
		ScrollX:       l.scrollX,
		ScrollY:       l.scrollY,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values cannot fail for any reason
		// the caller could act on, and returning an error here would push a
		// dead branch into every caller.
		panic("layer2: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the layer untouched rather than half-rewound — a half-rewound layer
// would keep drawing, from a mixture of two frames.
func (l *Layer2) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("layer2: empty state (the capture failed)")
	}
	var s layer2State
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("layer2: decoding state: %w", err)
	}

	l.activeBank = s.ActiveBank
	l.shadowBank = s.ShadowBank
	l.enabled = s.Enabled
	l.resolution = s.Resolution
	l.paletteOffset = s.PaletteOffset
	l.scrollX = s.ScrollX
	l.scrollY = s.ScrollY
	return nil
}
