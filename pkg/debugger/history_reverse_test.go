package debugger

import "testing"

// Reverse debugging reads the machine's past out of this ring, so what the
// ring fails to record is simply not visible when stepping backwards. Two
// things were missing for that: the shadow registers, which EXX and EX AF,AF'
// make part of the visible state, and a window on the stack, without which a
// backwards step through a CALL/RET sequence cannot show what was pushed.
//
// The ring is also a hot path, roughly a million pushes a second, and
// documented as allocation-free. That is why the stack window is a fixed array
// rather than a slice: a slice per entry would allocate on every instruction.

func TestHistoryEntryCarriesTheShadowRegisters(t *testing.T) {
	h := NewHistoryWide(4)
	h.Push(HistoryEntry{
		PC: 0x8000, AltAF: 0x1234, AltBC: 0x5678, AltDE: 0x9ABC, AltHL: 0xDEF0,
	})

	got := h.Last(1)
	if len(got) != 1 {
		t.Fatalf("Last(1) returned %d entries", len(got))
	}
	e := got[0]
	if e.AltAF != 0x1234 || e.AltBC != 0x5678 || e.AltDE != 0x9ABC || e.AltHL != 0xDEF0 {
		t.Errorf("shadow registers not round-tripped: AF'=%04X BC'=%04X DE'=%04X HL'=%04X",
			e.AltAF, e.AltBC, e.AltDE, e.AltHL)
	}
}

// The stack window is what makes a backwards step through a call sequence
// legible: the return address is on the stack, not in a register.
func TestHistoryEntryCarriesAStackWindow(t *testing.T) {
	var st [StackWindow]uint16
	for i := range st {
		st[i] = uint16(0x1000 + i)
	}
	h := NewHistoryWide(4)
	h.Push(HistoryEntry{PC: 0x8000, SP: 0xFF00, Stack: st})

	e := h.Last(1)[0]
	for i := range st {
		if e.Stack[i] != st[i] {
			t.Fatalf("stack word %d = %04X, want %04X", i, e.Stack[i], st[i])
		}
	}
}

// A reverse cursor needs random access by position, which Last cannot give
// without copying the whole ring on every step.
func TestAtIndexesFromTheOldestEntry(t *testing.T) {
	h := NewHistory(8)
	for i := 0; i < 5; i++ {
		h.Push(HistoryEntry{PC: uint16(0x100 + i), Insns: uint64(i)})
	}

	for i := 0; i < 5; i++ {
		e, ok := h.At(i)
		if !ok {
			t.Fatalf("At(%d) reported no entry, want one", i)
		}
		if e.PC != uint16(0x100+i) {
			t.Errorf("At(%d).PC = %04X, want %04X", i, e.PC, 0x100+i)
		}
	}
}

// Once the ring has wrapped, index 0 must be the oldest entry still held, not
// the oldest ever pushed. Getting this wrong makes a reverse cursor walk into
// entries that have already been overwritten.
func TestAtIndexesFromTheOldestSURVIVINGEntryAfterWrapping(t *testing.T) {
	h := NewHistory(4)
	for i := 0; i < 6; i++ { // two more than capacity
		h.Push(HistoryEntry{PC: uint16(0x100 + i), Insns: uint64(i)})
	}

	if h.Len() != 4 {
		t.Fatalf("Len = %d, want 4", h.Len())
	}
	// Entries 0 and 1 were overwritten; the oldest surviving is 2.
	e, ok := h.At(0)
	if !ok || e.PC != 0x102 {
		t.Errorf("At(0).PC = %04X (ok=%v), want %04X: index 0 must be the oldest SURVIVING entry",
			e.PC, ok, 0x102)
	}
	e, _ = h.At(3)
	if e.PC != 0x105 {
		t.Errorf("At(3).PC = %04X, want %04X: index Len-1 must be the newest", e.PC, 0x105)
	}
}

func TestAtRefusesAnOutOfRangeIndex(t *testing.T) {
	h := NewHistory(4)
	h.Push(HistoryEntry{PC: 0x100})

	for _, i := range []int{-1, 1, 99} {
		if _, ok := h.At(i); ok {
			t.Errorf("At(%d) reported an entry, want none: the cursor must not read past the ring", i)
		}
	}
}

func TestAtOnAnEmptyOrDisabledHistoryReportsNothing(t *testing.T) {
	if _, ok := NewHistory(0).At(0); ok {
		t.Error("a disabled history must report no entries")
	}
	if _, ok := NewHistory(8).At(0); ok {
		t.Error("an empty history must report no entries")
	}
}

// Push is on the emulator's hot path and documented as allocation-free.
// Widening the entry must not have changed that.
func TestPushDoesNotAllocate(t *testing.T) {
	h := NewHistoryWide(1024)
	e := HistoryEntry{PC: 0x8000, SP: 0xFF00}

	if n := testing.AllocsPerRun(200, func() { h.Push(e) }); n != 0 {
		t.Errorf("Push allocated %.0f times per call, want 0: the ring is pushed ~1M times a second", n)
	}
}
