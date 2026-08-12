package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// TestActiveVideoLine verifies the live raster-line counter exposed via
// NextReg $1E/$1F. NextZXOS dot commands (e.g. NextGuide) DISABLE
// interrupts and poll it to wait for the raster, so the crucial property
// is that it advances as the CPU's T-state counter advances within the
// frame — otherwise the wait loop hangs forever (the "frozen Guide" bug).
func TestActiveVideoLine(t *testing.T) {
	var ts uint64
	mem := &memory.Memory{}
	mem.TStates = &ts
	u := &ULA{mem: mem, frameStartTstate: 100}

	// Part-way through line 5.
	ts = 100 + uint64(TStatesPerLine*5+50)
	if got := u.ActiveVideoLine(); got != 5 {
		t.Errorf("ActiveVideoLine = %d, want 5", got)
	}
	// Advances with T-states — the property the DI'd raster-wait needs.
	ts = 100 + uint64(TStatesPerLine*7)
	if got := u.ActiveVideoLine(); got != 7 {
		t.Errorf("ActiveVideoLine = %d, want 7 (must advance)", got)
	}
	// The counter wraps at the FRAME, not at 9 bits. This previously asserted
	// `513 & 0x1FF == 1`, which encoded a bug: 511 is not a raster position,
	// and software polling NextReg $1E/$1F for a scanline compares against a
	// line that has to exist. Line 513 is line 513-311 = 202 of the next
	// frame.
	ts = 100 + uint64(TStatesPerLine*513)
	if got, want := u.ActiveVideoLine(), 513-LinesPerFrame; got != want {
		t.Errorf("ActiveVideoLine = %d, want %d (wraps at the frame)", got, want)
	}
	// No T-state source → 0, not a panic.
	if got := (&ULA{}).ActiveVideoLine(); got != 0 {
		t.Errorf("ActiveVideoLine with nil mem = %d, want 0", got)
	}
}

// TestBeamPosition verifies the per-T-state beam-position model: scanline
// and 8-pixel hpos derived from the T-state counter (the foundation for
// per-T-state Copper / contention / ULA-per-scanline).
func TestBeamPosition(t *testing.T) {
	var ts uint64
	mem := &memory.Memory{}
	mem.TStates = &ts
	u := &ULA{mem: mem, frameStartTstate: 100}

	// Part-way through line 5: T-state offset 40 → hpos 40/4 = 10.
	ts = 100 + uint64(TStatesPerLine*5+40)
	if line, hpos := u.BeamPosition(); line != 5 || hpos != 10 {
		t.Errorf("mid line 5: got line=%d hpos=%d, want 5,10", line, hpos)
	}
	// Top-left of the frame.
	ts = 100
	if line, hpos := u.BeamPosition(); line != 0 || hpos != 0 {
		t.Errorf("frame start: got line=%d hpos=%d, want 0,0", line, hpos)
	}
	// ActiveVideoLine stays consistent with BeamPosition's line.
	ts = 100 + uint64(TStatesPerLine*7+200)
	if u.ActiveVideoLine() != 7 {
		t.Errorf("ActiveVideoLine = %d, want 7", u.ActiveVideoLine())
	}
	// No T-state source → (0,0), not a panic.
	if line, hpos := (&ULA{}).BeamPosition(); line != 0 || hpos != 0 {
		t.Errorf("nil mem: got line=%d hpos=%d, want 0,0", line, hpos)
	}
}
