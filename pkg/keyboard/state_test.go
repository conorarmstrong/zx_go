package keyboard

import (
	"bytes"
	"testing"

	"fyne.io/fyne/v2"
)

// The keyboard looks like the easiest device in the machine to capture: eight
// bytes of matrix and nothing else. It isn't.
//
// TypeRune overlays a SYMBOL-SHIFT combination on the matrix for a couple of
// 50 Hz frames, and neither the overlay nor its remaining frame count is
// readable by the guest or stored in any snapshot format. Rewind into the
// middle of a pulse and restore only the matrix, and the symbol either never
// arrives or never lifts — a key stuck down for the rest of the session, which
// is exactly the failure this device is worth capturing for.
//
// These tests are a replay property rather than a field-by-field comparison,
// because a field-by-field test only checks the fields someone remembered.

// observe records everything anyone can see of the keyboard at one instant:
// each matrix row as the guest reads it through Scan (which overlays the
// symbol pulse), plus the BREAK flag the UI reads.
func observe(k *Keyboard) []byte {
	out := make([]byte, 0, 9)
	for row := 0; row < 8; row++ {
		addrHi := byte(^(1 << uint(row))) // active low: only this row selected
		out = append(out, k.Scan(uint16(addrHi)<<8|0x00FE))
	}
	if k.IsBreakPressed() {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

// primed returns a keyboard part-way through a symbol pulse with keys held
// down, so its hidden state holds values no matrix-only capture could rebuild.
func primed() *Keyboard {
	k := New()
	k.HandleKeyWithModifiers("a", true, false, false, false, false)         // row 1, bit 0
	k.HandleKeyWithModifiers(fyne.KeyB, true, false, false, false, false)   // row 7, bit 4
	k.HandleKeyWithModifiers(fyne.KeyF11, true, false, false, false, false) // BREAK: CAPS SHIFT + SPACE
	// '.' is SYMBOL SHIFT + M, held for runePulseFrames frames. Consuming one
	// frame here leaves the pulse mid-flight: still applied, but with a count
	// that differs from the value TypeRune set, so a capture that restored the
	// overlay but not the counter would still be caught.
	k.TypeRune('.')
	k.Tick()
	return k
}

// drive runs the keyboard forward the way the machine does — one Tick per
// frame, with host key events arriving in between — and records what is
// observable at every step.
//
// It releases keys the fixture was holding. That matters: if the forward run
// only ever pressed keys, the matrix would end up where the restore would have
// put it anyway, and a missing matrix restore would go unnoticed.
func drive(k *Keyboard) []byte {
	out := make([]byte, 0, 64)
	for frame := 0; frame < 5; frame++ {
		out = append(out, observe(k)...)
		switch frame {
		case 1:
			k.HandleKeyWithModifiers("a", false, false, false, false, false)
		case 2:
			k.HandleKeyWithModifiers(fyne.KeyF11, false, false, false, false, false)
		}
		k.Tick()
	}
	return append(out, observe(k)...)
}

// The property that matters: from a captured state, the keyboard must present
// the guest with the same sequence of scans it presented the first time.
func TestReplayingFromCapturedStateReproducesTheSameScans(t *testing.T) {
	k := primed()

	st := k.SaveState()
	first := drive(k)

	if err := k.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(k)

	if !bytes.Equal(first, second) {
		t.Errorf("the keyboard scanned differently on replay from the same captured state: "+
			"some part of the matrix state is not being captured\nfirst  %v\nsecond %v", first, second)
	}
}

// The negative that gives the test above its teeth. The matrix alone does
// round-trip, so a matrix-only capture would sail through a weak test. This
// proves the scans actually depend on more than the matrix.
func TestTheMatrixAloneIsNotEnoughToReproduceTheScans(t *testing.T) {
	k := primed()
	st := k.SaveState()

	k.matrixMu.RLock()
	raw := k.matrix
	k.matrixMu.RUnlock()

	want := drive(k)

	// Rebuild from the guest-visible matrix only, the way a snapshot format
	// does: no pulse overlay, no pulse counter, no BREAK flag.
	b := New()
	for row := 0; row < 8; row++ {
		for bit := byte(0x01); bit <= 0x10; bit <<= 1 {
			b.PressMatrixKey(row, bit, raw[row]&bit == 0)
		}
	}
	if bytes.Equal(want, drive(b)) {
		t.Skip("this fixture's scans happen not to depend on hidden state; " +
			"the replay test above is the binding one")
	}

	// ...and the full capture does what the matrix alone could not.
	if err := k.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, drive(k)) {
		t.Error("full state capture failed to reproduce the scans it was taken from")
	}
}

// A capture must not alias the live keyboard. The registry copies too, but a
// device handing back a view of its own memory is a bug worth catching here.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	k := primed()
	st := k.SaveState()
	before := observe(k)

	// Everything after the capture must be invisible to it.
	k.ReleaseAll()
	k.HandleKeyWithModifiers(fyne.KeyF11, false, false, false, false, false)
	k.TypeRune(',')
	for i := 0; i < 10; i++ {
		k.Tick()
	}

	if err := k.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := observe(k); !bytes.Equal(got, before) {
		t.Errorf("observation after restore = %v, want %v", got, before)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "keyboard" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "keyboard")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	k := primed()
	before := observe(k)

	if err := k.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if got := observe(k); !bytes.Equal(got, before) {
		t.Errorf("a rejected state blob changed the keyboard: %v, want %v (unchanged)", got, before)
	}
}

// A truncated blob is the shape a real corruption takes, and it must be
// rejected just as loudly as rubbish.
func TestLoadStateRejectsTruncatedState(t *testing.T) {
	k := primed()
	st := k.SaveState()
	before := observe(k)

	if err := k.LoadState(st[:len(st)/2]); err == nil {
		t.Error("a truncated state blob must be reported, not half-applied")
	}
	if got := observe(k); !bytes.Equal(got, before) {
		t.Errorf("a rejected state blob changed the keyboard: %v, want %v (unchanged)", got, before)
	}
}
