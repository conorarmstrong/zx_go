package lores

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// lores.vhd is combinational: it has no counters, no phase and nothing the
// guest cannot write, so everything the layer holds is in Config. That does
// not make the capture uninteresting, because what these ten registers mean
// depends on when they are read. A raster-split program rewrites the scroll
// offsets, the mode bit and the clip window part way down the frame, so the
// values that must come back are a scroll offset that has already wrapped, an
// address fold that is engaged and a clip window part way round its four
// coordinates — not the values the program set up before the frame began.
//
// What is NOT here, and why:
//
//   - The raster position (hc, vc). It is an argument to Pixel and
//     RenderScanline, not a field, because on the FPGA it arrives on a port
//     from the video timing generator. Whichever device owns the raster
//     captures it; storing a copy here would restore a second, disagreeing
//     idea of where the beam is.
//
//   - The display bytes. The layer reads bank 5 through a BankReader every
//     scanline, exactly as the FPGA's VRAM arbiter does (zxnext.vhd:4266-4267);
//     the bank belongs to pkg/memory and is captured there. A copy here would
//     be 16 KB per rewind point of a value that is already in the state, and
//     the two copies would drift.
//
//   - The clip-window WRITE INDEX. This is the one worth being precise about,
//     because the layer does have a clip window and the window does have an
//     index. On the Next each clip window is four coordinates behind a 2-bit
//     index that a write advances and NR$1C resets (zxnext.vhd:5242-5290).
//     That index is register-file state, not layer state: it lives in the
//     NextReg wiring (pkg/next/wire.go, clipWindow), which owns NR$18-$1C for
//     the Layer 2, sprite, ULA/LoRes and tilemap windows together, and it is
//     captured by whichever device captures the NextReg dispatcher. What
//     reaches this layer is the four resolved coordinates, which is exactly
//     what lores.vhd's clip_x1_i..clip_y2_i ports carry. Capturing the index
//     here as well would put one register in two blobs, and a restore would
//     leave whichever was applied last — the failure mode the machinestate
//     registry exists to prevent. The index is not faked here; it is somebody
//     else's field.
//
// The layer has no lock; like every other access to it (register writes,
// RenderScanline), SaveState and LoadState belong on the emulation goroutine.

// loresState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of Config is present: adding a
// register to Config without adding it here is the failure this package's
// tests are built to catch.
type loresState struct {
	Radastan      bool
	Dfile         bool
	ULAPlus       bool
	PaletteOffset uint8

	ScrollX uint8
	ScrollY uint8

	ClipX1 uint8
	ClipX2 uint8
	ClipY1 uint8
	ClipY2 uint8
}

// StateID identifies the LoRes layer in a captured machine state.
func (c *Config) StateID() string { return "next.lores" }

// SaveState captures the complete LoRes layer state.
func (c *Config) SaveState() []byte {
	s := loresState{
		Radastan:      c.Radastan,
		Dfile:         c.Dfile,
		ULAPlus:       c.ULAPlus,
		PaletteOffset: c.PaletteOffset,
		ScrollX:       c.ScrollX,
		ScrollY:       c.ScrollY,
		ClipX1:        c.ClipX1,
		ClipX2:        c.ClipX2,
		ClipY1:        c.ClipY1,
		ClipY2:        c.ClipY2,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding ten fixed-size values cannot fail in practice, and this runs
		// from the capture hook, which deliberately skips a failed capture
		// rather than stopping the machine. A nil blob is rejected by
		// LoadState, so a bad capture surfaces as a failed restore instead of
		// taking the emulator down mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the layer untouched rather than drawing the rest of the frame with a
// scroll offset from the capture and a clip window from the present.
func (c *Config) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("lores: empty state (the capture failed)")
	}
	var s loresState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("lores: decoding state: %w", err)
	}

	c.Radastan = s.Radastan
	c.Dfile = s.Dfile
	c.ULAPlus = s.ULAPlus
	c.PaletteOffset = s.PaletteOffset
	c.ScrollX = s.ScrollX
	c.ScrollY = s.ScrollY
	c.ClipX1 = s.ClipX1
	c.ClipX2 = s.ClipX2
	c.ClipY1 = s.ClipY1
	c.ClipY2 = s.ClipY2
	return nil
}
