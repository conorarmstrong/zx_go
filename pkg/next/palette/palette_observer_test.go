package palette

import "testing"

// The raster journal needs to know exactly what a palette write replaced so
// it can rewind to the frame-start state and replay the change at the row it
// was made on. The Bank reports each entry write to an observer.

func TestWriteObserverReportsWrite8(t *testing.T) {
	b := NewBank()
	b.Select(PaletteLayer2First)
	b.SetIndex(3)
	b.Active().Set(3, 0x155)

	var got []PaletteWrite
	b.SetWriteObserver(func(w PaletteWrite) { got = append(got, w) })

	b.Write8(0xAA)

	if len(got) != 1 {
		t.Fatalf("observer fired %d times, want 1", len(got))
	}
	w := got[0]
	if w.Slot != PaletteLayer2First || w.Index != 3 {
		t.Errorf("slot/index = %d/%d, want %d/3", w.Slot, w.Index, PaletteLayer2First)
	}
	if w.OldVal != 0x155 {
		t.Errorf("OldVal = %#x, want 0x155", w.OldVal)
	}
	// Write8: value<<1, low bit set when either of the written low 2 bits is.
	if want := uint16(0xAA)<<1 | 1; w.NewVal != want {
		t.Errorf("NewVal = %#x, want %#x", w.NewVal, want)
	}
}

func TestWriteObserverReportsWrite9WithPriority(t *testing.T) {
	b := NewBank()
	b.Select(PaletteLayer2First)
	b.SetIndex(9)
	b.Active().Set(9, 0x0F0)
	b.Active().SetPriority(9, 0x01)

	var got PaletteWrite
	n := 0
	b.SetWriteObserver(func(w PaletteWrite) { got, n = w, n+1 })

	b.Write9(0x81, 0xC1) // lo bits 7:6 = priority 3, lo bit 0 = 1

	if n != 1 {
		t.Fatalf("observer fired %d times, want 1", n)
	}
	if got.OldVal != 0x0F0 || got.OldPrio != 0x01 {
		t.Errorf("old = %#x/%d, want 0x0F0/1", got.OldVal, got.OldPrio)
	}
	if want := uint16(0x81)<<1 | 1; got.NewVal != want {
		t.Errorf("NewVal = %#x, want %#x", got.NewVal, want)
	}
	if got.NewPrio != 0x03 {
		t.Errorf("NewPrio = %d, want 3", got.NewPrio)
	}
}

// TestWriteObserverUndoRestoresTheEntry pins the property the journal relies
// on: applying the reported old values puts the palette back exactly.
func TestWriteObserverUndoRestoresTheEntry(t *testing.T) {
	b := NewBank()
	b.Select(PaletteSpritesFirst)
	b.SetIndex(0)
	b.Active().Set(0, 0x123)
	b.Active().SetPriority(0, 0x02)

	var w PaletteWrite
	b.SetWriteObserver(func(x PaletteWrite) { w = x })
	b.Write8(0x55)

	if b.Palette(int(w.Slot)).Get(w.Index) == w.OldVal {
		t.Fatal("precondition: the write did not change the entry")
	}
	b.Palette(int(w.Slot)).Set(w.Index, w.OldVal)
	b.Palette(int(w.Slot)).SetPriority(w.Index, w.OldPrio)
	if got := b.Palette(int(w.Slot)).Get(0); got != 0x123 {
		t.Errorf("after undo entry = %#x, want 0x123", got)
	}
	if got := b.Palette(int(w.Slot)).Priority(0); got != 0x02 {
		t.Errorf("after undo priority = %d, want 2", got)
	}
}

// TestNoObserverIsSafe guards the default path.
func TestNoObserverIsSafe(t *testing.T) {
	b := NewBank()
	b.Write8(0x11)
	b.Write9(0x22, 0x33)
}
