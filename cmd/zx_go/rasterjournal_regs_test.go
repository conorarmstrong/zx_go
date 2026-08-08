package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// v1.6.0 journalled palette writes and Layer 2 scroll, which covers the two
// dominant CPU-driven raster effects, but left every other purely-visual
// NextReg latching once per frame. These pin the rest.
//
// Each of these registers has an idempotent, purely-visual handler: replaying
// it cannot auto-increment a cursor, upload sprite data, or page memory. That
// is the whole reason a whitelist exists — see journalVisualRegs.

// journalledRegs is the set the wiring is expected to cover, with a value
// that is guaranteed to differ from the reset state.
var journalledRegs = []struct {
	reg  byte
	val  byte
	name string
}{

	{0x14, 0x07, "global transparency"},
	{0x15, 0x04, "layer priority"},
	{0x2F, 0x02, "tilemap X offset (MSB)"},
	{0x30, 0x40, "tilemap Y offset"},
	{0x4A, 0x55, "fallback colour"},
	{0x4C, 0x09, "tilemap transparency"},
	{0x6E, 0x20, "tilemap base"},
	{0x6F, 0x2E, "tile definitions base"},
}

// TestVisualRegistersAreJournalledAtTheirRow pins that a mid-frame CPU write
// to each register is recorded against the display row it was made on, and
// that rewinding restores the value it replaced.
func TestVisualRegistersAreJournalledAtTheirRow(t *testing.T) {
	for _, r := range journalledRegs {
		r := r
		t.Run(r.name, func(t *testing.T) {
			emu := nextEmuForRaster(t)
			if emu.nextRegs == nil {
				t.Skip("NextRegs not wired on this build")
			}
			log := emu.ula.NextRasterLogForTest()
			if log == nil {
				t.Fatal("no raster journal wired")
			}

			// Park the raster partway down, then write.
			start := roms.DisplayStartTState(roms.ModelNext)
			perLine := roms.TStatesPerLine(roms.ModelNext)
			*emu.mem.TStates = uint64(start + 80*perLine)

			before := emu.nextRegs.Raw(r.reg)
			if before == r.val {
				t.Fatalf("test value %#02x equals the reset value; pick another", r.val)
			}
			log.EndReplay() // clear anything from construction
			emu.nextRegs.WriteReg(r.reg, r.val)

			if log.Len() == 0 {
				t.Fatalf("NR$%02X write was not journalled", r.reg)
			}

			log.BeginReplay()
			if got := emu.nextRegs.Raw(r.reg); got != before {
				t.Errorf("after rewind NR$%02X = %#02x, want the frame-start %#02x",
					r.reg, got, before)
			}
			log.ApplyThrough(79)
			if got := emu.nextRegs.Raw(r.reg); got != before {
				t.Errorf("row 79 (above the write) NR$%02X = %#02x, want %#02x",
					r.reg, got, before)
			}
			log.ApplyThrough(80)
			if got := emu.nextRegs.Raw(r.reg); got == before {
				t.Errorf("row 80 (the write's row) NR$%02X still %#02x; the change did not replay",
					r.reg, got)
			}
			log.EndReplay()
		})
	}
}

// TestJournalReplayDoesNotDoubleApply guards the property that makes replaying
// a register handler safe at all: running it twice must land in the same place
// as running it once. A register whose handler auto-increments a cursor or
// uploads data would fail this, and must never be added to the whitelist.
func TestJournalReplayDoesNotDoubleApply(t *testing.T) {
	for _, r := range journalledRegs {
		r := r
		t.Run(r.name, func(t *testing.T) {
			emu := nextEmuForRaster(t)
			if emu.nextRegs == nil {
				t.Skip("NextRegs not wired on this build")
			}
			emu.nextRegs.WriteReg(r.reg, r.val)
			once := emu.nextRegs.Raw(r.reg)
			emu.nextRegs.WriteReg(r.reg, r.val)
			if twice := emu.nextRegs.Raw(r.reg); twice != once {
				t.Errorf("NR$%02X is not idempotent: %#02x then %#02x", r.reg, once, twice)
			}
		})
	}
}
