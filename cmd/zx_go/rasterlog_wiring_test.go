package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/rasterlog"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The Next composites its layers at the end of a frame, so a CPU write to a
// visual register partway down the screen used to apply retroactively to the
// whole frame — a raster split driven from an interrupt handler or a
// raster-wait loop simply did not appear. (The Copper was never affected: it
// is stepped inside the compositor's row loop.)
//
// These check the wiring end to end on a real Next core: that the journal is
// attached, that the observers feed it, and that the row it stamps a write
// with follows the CPU's position in the frame.

// nextEmuForRaster builds a Next emulator, skipping if its ROMs are not
// installed on this machine.
func nextEmuForRaster(t *testing.T) *emulator {
	t.Helper()
	emu, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	if emu.nextPalette == nil {
		t.Skip("Next palette not wired on this build")
	}
	return emu
}

// TestPaletteWriteIsJournalledAtTheCPURow pins that a palette write is
// stamped with the display row the raster is on, not applied frame-wide.
func TestPaletteWriteIsJournalledAtTheCPURow(t *testing.T) {
	emu := nextEmuForRaster(t)
	pal := emu.nextPalette

	// Park the CPU clock partway down the display.
	start := roms.DisplayStartTState(roms.ModelNext)
	perLine := roms.TStatesPerLine(roms.ModelNext)
	*emu.mem.TStates = uint64(start + 100*perLine)
	if got := emu.ula.DisplayRow(); got != 100 {
		t.Fatalf("DisplayRow = %d, want 100", got)
	}

	pal.Select(palette.PaletteLayer2First)
	pal.SetIndex(4)
	pal.Active().Set(4, 0x000)
	pal.Write8(0xFF)

	// The live value is the new one; rewinding must restore what it replaced,
	// and replaying up to row 99 must NOT bring it back.
	if got := pal.Palette(int(palette.PaletteLayer2First)).Get(4); got == 0x000 {
		t.Fatal("precondition: the palette write did not land")
	}
	live := pal.Palette(int(palette.PaletteLayer2First)).Get(4)

	log := emu.ula.NextRasterLogForTest()
	if log == nil {
		t.Fatal("no raster journal wired to the Next ULA")
	}
	if log.Len() == 0 {
		t.Fatal("the palette write was not journalled")
	}

	log.BeginReplay()
	if got := pal.Palette(int(palette.PaletteLayer2First)).Get(4); got != 0x000 {
		t.Errorf("after Rewind entry = %#x, want the frame-start 0x000", got)
	}
	log.ApplyThrough(99)
	if got := pal.Palette(int(palette.PaletteLayer2First)).Get(4); got != 0x000 {
		t.Errorf("row 99 (above the write) entry = %#x, want 0x000", got)
	}
	log.ApplyThrough(100)
	if got := pal.Palette(int(palette.PaletteLayer2First)).Get(4); got != live {
		t.Errorf("row 100 (the write's row) entry = %#x, want %#x", got, live)
	}
	log.EndReplay()
	if got := pal.Palette(int(palette.PaletteLayer2First)).Get(4); got != live {
		t.Errorf("after ApplyAll entry = %#x, want the live %#x", got, live)
	}
}

// TestLayer2ScrollIsJournalled pins the other wired source: per-line parallax
// written from the CPU.
func TestLayer2ScrollIsJournalled(t *testing.T) {
	emu := nextEmuForRaster(t)
	if emu.nextLayer2 == nil {
		t.Skip("Layer 2 not wired on this build")
	}
	l2 := emu.nextLayer2

	start := roms.DisplayStartTState(roms.ModelNext)
	perLine := roms.TStatesPerLine(roms.ModelNext)

	l2.SetScrollX(0)
	log := emu.ula.NextRasterLogForTest()
	if log == nil {
		t.Fatal("no raster journal wired")
	}
	log.EndReplay()

	*emu.mem.TStates = uint64(start + 50*perLine)
	l2.SetScrollX(64)

	if log.Len() == 0 {
		t.Fatal("the scroll write was not journalled")
	}
	log.BeginReplay()
	if got := l2.ScrollX(); got != 0 {
		t.Errorf("after Rewind scrollX = %d, want 0", got)
	}
	log.ApplyThrough(49)
	if got := l2.ScrollX(); got != 0 {
		t.Errorf("row 49 scrollX = %d, want 0", got)
	}
	log.ApplyThrough(50)
	if got := l2.ScrollX(); got != 64 {
		t.Errorf("row 50 scrollX = %d, want 64", got)
	}
}

// TestDisplayRowTracksTheFrame pins the row source the journal timestamps
// with, including the out-of-display sentinels.
func TestDisplayRowTracksTheFrame(t *testing.T) {
	emu := nextEmuForRaster(t)
	start := roms.DisplayStartTState(roms.ModelNext)
	perLine := roms.TStatesPerLine(roms.ModelNext)

	for _, tc := range []struct {
		name string
		t    int
		want int
	}{
		{"interrupt", 0, rasterlog.RowBeforeDisplay},
		{"one before the display", start - 1, rasterlog.RowBeforeDisplay},
		{"first display row", start, 0},
		{"row 1", start + perLine, 1},
		{"last display row", start + 191*perLine, 191},
		{"below the display", start + 192*perLine, rasterlog.RowAfterDisplay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*emu.mem.TStates = uint64(tc.t)
			if got := emu.ula.DisplayRow(); got != tc.want {
				t.Errorf("DisplayRow at T=%d = %d, want %d", tc.t, got, tc.want)
			}
		})
	}
}
