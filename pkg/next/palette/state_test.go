package palette

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*Bank)(nil)

// The palette is the device that proves a rewind needs more than the tables.
//
// Eight 256-entry tables are the bulk of the bytes but not the part that
// decides what the next guest write does. That comes from the write cursor, the
// NR$43 write-target selection, the auto-increment-disable latch and — the one
// with teeth — the NR$44 two-byte pairing phase. A capture taken between the
// two halves of an NR$44 write and restored without its phase resumes with the
// halves swapped: every subsequent pair commits the wrong byte into the wrong
// entry, and the picture is wrong from that point on rather than at that point.
// A Copper phase bug of exactly that shape was a real crash.
//
// These tests are a replay property, not a field-by-field comparison, because a
// field-by-field test only checks the fields someone remembered to add.

// primed returns a Bank part way through several operations at once: four of
// the eight tables uploaded through the guest path, per-layer render selections
// away from their defaults, a non-zero write cursor, auto-increment disabled
// mid-sequence, and — the point of the fixture — an NR$44 pair with its high
// byte latched and its second byte not yet written.
func primed() *Bank {
	b := NewBank()

	// Upload real content through the guest write path, so entries AND
	// priorities hold values no default could reconstruct.
	for _, slot := range []byte{PaletteULAFirst, PaletteLayer2Second, PaletteSpritesFirst, PaletteTilemapSecond} {
		b.Select(slot)
		b.SetIndex(0)
		for i := 0; i < 256; i++ {
			b.Write8(byte(i*7 + int(slot)*13))
		}
		// A run of NR$44 pairs, so the 2-bit priorities are not all zero.
		// Priority lives in the second byte's bits 7:6, so 0x40/0x80/0xC0 are
		// the values that make this fixture bite; a second byte of 0x00 would
		// write priority 0 everywhere and take priority out of the test.
		b.SetIndex(0x10)
		for i := 0; i < 8; i++ {
			b.WriteNR44(byte(0x30 + i))
			b.WriteNR44(byte((i%4)<<6 | (i & 1)))
		}
	}

	// Per-layer render selections, all away from the freshly-constructed 0.
	b.SetActive(LayerULA, 0)
	b.SetActive(LayerLayer2, 1)
	b.SetActive(LayerSprites, 1)
	b.SetActive(LayerTilemap, 1)

	// Mid-operation: writing into the sprites table, cursor part way up it,
	// auto-increment disabled (NR$43 bit 7) so the next writes stay put...
	b.Select(PaletteSpritesFirst)
	b.SetIndex(0x7E)
	b.SetAutoIncDisable(true)

	// ...and half of an NR$44 pair written. The high byte is latched; the
	// entry has not been touched yet.
	b.WriteNR44(0x81)

	return b
}

// drive runs a fixed script of guest-visible palette operations and returns a
// transcript of everything observable while it runs: the read-back registers
// after each step, every table's contents at a spread of indices, the per-layer
// render selections, and the write-observer stream the raster journal consumes.
//
// The script starts by completing the pair the fixture left half-written, which
// is what makes the pairing phase load-bearing: resume with the phase wrong and
// every write after it lands somewhere else.
func drive(b *Bank) []byte {
	var tr []byte
	var writes []PaletteWrite
	b.SetWriteObserver(func(w PaletteWrite) { writes = append(writes, w) })

	rec := func(tag string) {
		tr = append(tr, tag...)
		tr = append(tr, b.Selected(), b.Index(), b.Read8(), b.ReadNR44())
		for _, l := range []Layer{LayerULA, LayerLayer2, LayerSprites, LayerTilemap} {
			tr = append(tr, b.ActiveSelector(l))
		}
		for slot := 0; slot < 8; slot++ {
			p := b.Palette(slot)
			for _, i := range []byte{0x00, 0x10, 0x11, 0x3F, 0x7E, 0x7F, 0xFE, 0xFF} {
				v := p.Get(i)
				tr = append(tr, byte(v>>8), byte(v), p.Priority(i))
			}
		}
	}

	rec("start")

	// The second byte of the pair the fixture left open: priority 1, low bit 0.
	b.WriteNR44(0x40)
	rec("pair-commit")

	// Auto-increment is still disabled, so both land on the same entry.
	b.Write8(0x2A)
	b.Write8(0xD5)
	rec("autoinc-off")

	b.SetAutoIncDisable(false)
	b.Write8(0x11)
	b.Write8(0x22)
	rec("autoinc-on")

	// A fresh pair, left latched across a read-back...
	b.WriteNR44(0xC3)
	rec("pair-latched")
	b.WriteNR44(0x81) // priority 2, low bit 1
	rec("pair-done")

	// Switch write target and render selections, then run the cursor over the
	// 0xFF -> 0x00 wrap. Every layer's selection is moved, so a capture that
	// dropped one of the four would show up here rather than only in whichever
	// layer this script happens to touch.
	b.Select(PaletteTilemapSecond)
	b.SetActive(LayerULA, 1)
	b.SetActive(LayerLayer2, 0)
	b.SetActive(LayerSprites, 0)
	b.SetActive(LayerTilemap, 0)
	b.SetIndex(0xFE)
	b.Write8(0x77)
	b.Write8(0x88)
	b.Write8(0x99)
	rec("wrap")

	// One more complete pair, so the script ENDS with the latch disarmed while
	// the capture it replays from was taken with the latch armed. Leaving both
	// ends armed would let a restore that dropped the phase pass, because the
	// phase it inherited from the previous run would happen to match.
	b.WriteNR44(0x5A)
	b.WriteNR44(0x0F)
	rec("end")

	for _, w := range writes {
		tr = append(tr,
			w.Slot, w.Index,
			byte(w.OldVal>>8), byte(w.OldVal),
			byte(w.NewVal>>8), byte(w.NewVal),
			w.OldPrio, w.NewPrio)
	}
	return tr
}

// The property that matters: from a captured state, the bank must do exactly
// what it did the first time.
func TestReplayingFromCapturedStateReproducesTheSameWrites(t *testing.T) {
	b := primed()

	st := b.SaveState()
	first := drive(b)

	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(b)

	if !bytes.Equal(first, second) {
		t.Error("the palette behaved differently on replay from the same captured state: " +
			"some part of the bank is not being captured")
	}
}

// The negative that gives the test above its teeth. The eight tables, the
// cursor and the write-target selection all round-trip through the obvious
// capture, so a test that only checked those would pass while the NR$44 pairing
// phase was dropped. This proves the transcript actually depends on the phase.
func TestTheTablesAndCursorAloneAreNotEnough(t *testing.T) {
	b := primed()
	st := b.SaveState()
	want := drive(b)

	// Put b back at the capture point, then rebuild a second bank the way a
	// capture that forgot the pairing phase would: every table, the cursor, the
	// selection, the render selections and the auto-increment latch, but no
	// pending9/have9.
	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	c := NewBank()
	for slot := 0; slot < 8; slot++ {
		src, dst := b.Palette(slot), c.Palette(slot)
		for i := 0; i < 256; i++ {
			dst.Set(byte(i), src.Get(byte(i)))
			dst.SetPriority(byte(i), src.Priority(byte(i)))
		}
	}
	c.Select(b.Selected())
	c.SetIndex(b.Index())
	c.autoIncDisable = b.autoIncDisable
	for _, l := range []Layer{LayerULA, LayerLayer2, LayerSprites, LayerTilemap} {
		c.SetActive(l, b.ActiveSelector(l))
	}

	if bytes.Equal(want, drive(c)) {
		t.Fatal("a bank rebuilt without the NR$44 pairing phase replayed identically: " +
			"the fixture is not mid-pair, so this file proves nothing about phase")
	}

	// ...and the full capture does what the tables alone could not.
	if !bytes.Equal(want, drive(b)) {
		t.Error("full state capture failed to reproduce the run it was taken from")
	}
}

// A capture must not alias the live tables. The registry copies the blob too,
// but a device handing back a view of its own memory is a bug worth catching
// where it would be introduced.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	b := primed()
	st := b.SaveState()
	before := append([]byte(nil), st...)

	drive(b)
	for i := 0; i < 256; i++ {
		b.Palette(int(PaletteULAFirst)).Set(byte(i), 0x1FF)
		b.Palette(int(PaletteULAFirst)).SetPriority(byte(i), 3)
	}

	if !bytes.Equal(before, st) {
		t.Error("the captured blob changed when the bank did: the capture aliases live state")
	}
	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := b.Palette(int(PaletteULAFirst)).Get(0x3F); got == 0x1FF {
		t.Error("restore left the post-capture value in place")
	}
}

// The write observer belongs to whoever installed it — the raster journal, in
// practice. Rewinding the machine must not silently unhook it, or mid-frame
// palette changes stop being journalled from the rewind onwards.
func TestLoadStateLeavesTheWriteObserverAttached(t *testing.T) {
	b := primed()
	st := b.SaveState()

	n := 0
	b.SetWriteObserver(func(PaletteWrite) { n++ })
	if err := b.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	b.Write8(0x12)

	if n != 1 {
		t.Errorf("observer fired %d times after a restore, want 1: LoadState detached it", n)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := NewBank().StateID(); got != "next.palette" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.palette")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	b := primed()
	before := b.SaveState()

	if err := b.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Fatal("a malformed state blob must be reported, not half-applied")
	}
	if err := b.LoadState(nil); err == nil {
		t.Fatal("an empty state blob must be reported: it means the capture failed")
	}

	if !bytes.Equal(before, b.SaveState()) {
		t.Error("a rejected state blob changed the bank: it was half-applied")
	}
}
