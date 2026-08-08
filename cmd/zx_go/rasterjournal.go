package main

import (
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/rasterlog"
)

// visualJournalRegs are the NextRegs whose mid-frame writes are replayed at
// the row they were made on, rather than applying retroactively to the whole
// frame (see pkg/next/rasterlog).
//
// This is a WHITELIST, and it must stay one. Replay re-runs the register's own
// write handler, so a register only belongs here if that handler is purely
// visual and idempotent — running it twice must land where running it once
// does. Anything that auto-increments a cursor (the palette index at NR$40 /
// NR$41 / NR$44), uploads data (sprite attributes at NR$34+), or changes the
// memory map (the MMU at NR$50-57) would corrupt state on replay and must
// never be added. TestJournalReplayDoesNotDoubleApply guards the property.
//
// Palette entries and Layer 2 scroll are journalled too, but at the object
// level rather than here: the palette because its register path is exactly the
// auto-incrementing kind that cannot be replayed, and Layer 2 scroll because
// it is assembled from two registers (NR$16 and NR$71).
var visualJournalRegs = []byte{
	0x14, // global transparency colour
	0x15, // layer priority / sprite enables
	0x2F, // tilemap X offset, high bits
	0x30, // tilemap Y offset
	0x4A, // fallback colour
	0x4C, // tilemap transparency index
	0x6E, // tilemap base address
	0x6F, // tile definitions base address
}

// NR$14, the global transparency colour, is deliberately NOT journalled even
// though it is purely visual and idempotent.
//
// Transparency is decided by comparing a layer's pixel against NR$14, and the
// ULA layer this is compared against is rendered ONCE per frame, not per row.
// Making only the comparison per-row while the thing compared stays per-frame
// is not more faithful, it is inconsistent — and it shows: journalling NR$14
// blanks the show512 demo, which renders correctly when the frame-final value
// is used throughout. Revisit if the ULA layer ever becomes a per-scanline
// render; until then the two have to agree.

// journalVisualRegs wraps the already-installed write handler for each
// whitelisted register so the change is recorded against the current display
// row. Call it AFTER all the handlers are wired, since it captures them.
func journalVisualRegs(disp *nextregs.Dispatcher, rlog *rasterlog.Log) {
	for _, reg := range visualJournalRegs {
		reg := reg
		inner := disp.OnWriteFn(reg)
		if inner == nil {
			// No handler wired for this register on this build; the raw
			// store still happens, so record that.
			inner = func(d *nextregs.Dispatcher, val byte) { d.Store(reg, val) }
		}
		disp.SetOnWrite(reg, func(d *nextregs.Dispatcher, val byte) {
			old := d.Raw(reg)
			inner(d, val)
			if old == val {
				return // nothing moved; nothing to replay
			}
			rlog.Record(
				func() { inner(d, old) },
				func() { inner(d, val) },
			)
		})
	}
}
