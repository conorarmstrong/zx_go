package ula

import (
	"image"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Border changes land on the beam position of the write, not on the whole
// scanline. A beeper routine flips the border with every speaker pulse, many
// times per line (the "We Are Vocoders" demo writes $00 and $FF to port $FE
// in a tight loop). Real hardware draws those as dashes along each line;
// painting a line in the colour of its last write shows a solid border.
//
// The mapping follows Fuse (display_dirty.c display_get_beam_position,
// machine.c line_times): image row y starts at
//
//	displayStart - BorderTop lines - 16 T (the 32-pixel left border) + y*line
//
// and a write at T paints from column (T - rowStart) / step onward. The
// Sinclair ULAs latch the border colour once per 8-pixel column (4 T; FPGA
// zxula.vhd:423-429 reloads attr_reg only on sload); the Pentagon reloads it
// on every pixel (zxula.vhd:443-447), so there a write lands to the T-state,
// 2 pixels.

func newBorderULA(t *testing.T, model roms.SpectrumModel) (*ULA, *uint64) {
	t.Helper()
	dir := "test_roms_border_beam"
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := memory.New(dir, model)
	if err != nil {
		t.Fatal(err)
	}
	var ts uint64
	mem.TStates = &ts
	return New(mem, keyboard.New()), &ts
}

// rowStart is the T-state at which image row y's leftmost pixel is drawn.
func rowStart(model roms.SpectrumModel, y int) int {
	line := TStatesPerLineFor(model)
	return roms.DisplayStartTState(model) - BorderTop*line - BorderLeft/2 + y*line
}

// border writes colour to port $FE at frame T-state at.
func border(u *ULA, ts *uint64, at int, colour byte) {
	*ts = uint64(at)
	u.WritePort(0xFE, colour)
}

// wantSpan checks pixels [x0, x1) of row y are the palette colour c.
func wantSpan(t *testing.T, u *ULA, img *image.RGBA, y, x0, x1 int, c byte) {
	t.Helper()
	for x := x0; x < x1; x++ {
		if got := img.RGBAAt(x, y); got != u.palette[c] {
			t.Errorf("row %d x=%d: got %v, want colour %d", y, x, got, c)
			return
		}
	}
}

func TestBorderWriteLandsOnItsColumn48K(t *testing.T) {
	u, ts := newBorderULA(t, roms.Model48K)
	// 2 T into column 5 of row 10: the whole column takes the new colour.
	border(u, ts, rowStart(roms.Model48K, 10)+4*5+2, 2)
	img := u.Render()

	wantSpan(t, u, img, 9, 0, TotalWidth, 0)
	wantSpan(t, u, img, 10, 0, 40, 0)
	wantSpan(t, u, img, 10, 40, TotalWidth, 2)
	wantSpan(t, u, img, 11, 0, TotalWidth, 2)
}

// A pulse shorter than a line shows as a dash on that line only: the shape a
// beeper routine draws in the side border.
func TestBorderPulseDrawsADash(t *testing.T) {
	u, ts := newBorderULA(t, roms.Model48K)
	border(u, ts, 0, 7)
	y := BorderTop + 100 // a display row: border only either side
	border(u, ts, rowStart(roms.Model48K, y)+4*1, 0)
	border(u, ts, rowStart(roms.Model48K, y)+4*3, 7)
	img := u.Render()

	wantSpan(t, u, img, y, 0, 8, 7)
	wantSpan(t, u, img, y, 8, 24, 0)
	wantSpan(t, u, img, y, 24, BorderLeft, 7)
	wantSpan(t, u, img, y, BorderLeft+ScreenWidth, TotalWidth, 7)
	wantSpan(t, u, img, y-1, 0, BorderLeft, 7)
	wantSpan(t, u, img, y+1, 0, BorderLeft, 7)
}

// The right border belongs to the same line: a write in the right border
// changes it from that column and the rest of the frame after it.
func TestBorderWriteInRightBorder(t *testing.T) {
	u, ts := newBorderULA(t, roms.Model48K)
	y := BorderTop + 50
	col := (BorderLeft+ScreenWidth)/8 + 2 // third right-border column
	border(u, ts, rowStart(roms.Model48K, y)+4*col, 5)
	img := u.Render()

	wantSpan(t, u, img, y, BorderLeft+ScreenWidth, col*8, 0)
	wantSpan(t, u, img, y, col*8, TotalWidth, 5)
	wantSpan(t, u, img, y+1, 0, BorderLeft, 5)
}

func TestBorderWriteLandsOnItsColumn128K(t *testing.T) {
	u, ts := newBorderULA(t, roms.Model128K)
	border(u, ts, rowStart(roms.Model128K, 10)+4*5, 2)
	img := u.Render()

	wantSpan(t, u, img, 10, 0, 40, 0)
	wantSpan(t, u, img, 10, 40, TotalWidth, 2)
}

func TestBorderWriteLandsOnItsPixelPairPentagon(t *testing.T) {
	u, ts := newBorderULA(t, roms.ModelPentagon)
	border(u, ts, rowStart(roms.ModelPentagon, 10)+4*5+2, 2)
	img := u.Render()

	wantSpan(t, u, img, 10, 0, 44, 0)
	wantSpan(t, u, img, 10, 44, TotalWidth, 2)
}

// At a turbo CPU speed the counter runs SpeedMultiplier T-states per ULA
// T-state; the beam position is in ULA T-states.
func TestBorderWriteAtTurboSpeedUsesULATime(t *testing.T) {
	u, ts := newBorderULA(t, roms.Model128K)
	u.mem.SpeedMultiplier = func() int { return 4 }
	border(u, ts, 4*(rowStart(roms.Model128K, 10)+4*5), 2)
	img := u.Render()

	wantSpan(t, u, img, 10, 0, 40, 0)
	wantSpan(t, u, img, 10, 40, TotalWidth, 2)
}
