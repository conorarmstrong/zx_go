package layer2

import (
	"bytes"
	"testing"
)

// Layer 2 is a device whose state is only visible in what it draws, and the
// interesting moment to capture it is never at rest: the effects it exists for
// (per-line parallax, double buffering, a mid-frame resolution change) rewrite
// its registers between one scanline and the next. A capture taken between two
// of those writes has to resume the sequence, not restart it.
//
// So these tests are a replay property rather than a field-by-field
// comparison: drive the layer forward recording every pixel it renders and
// every scroll write it reports, restore, drive it forward identically, and
// require the two outputs to be identical. A field-by-field test only checks
// the fields someone remembered to add.

// drive runs n scanlines of the per-line effect a Layer 2 demo does — parallax
// scroll on every line, a double-buffer flip, a palette-offset cycle, a
// resolution change and an enable toggle — and returns everything an observer
// of the layer can see: the pixels rendered and the scroll writes reported.
//
// Every write is RELATIVE to the value already in the register, and the
// schedule leaves each one different from where it started (an odd number of
// bank flips, one enable toggle, two of the three-step resolution cycle). That
// is what gives the replay teeth: a field the restore forgot is still holding
// its drifted value, so the very next line renders differently.
func drive(l *Layer2, n int) []byte {
	var out []byte
	l.SetScrollObserver(func(w ScrollWrite) {
		out = append(out,
			byte(w.OldX>>8), byte(w.OldX), w.OldY,
			byte(w.NewX>>8), byte(w.NewX), w.NewY)
	})
	defer l.SetScrollObserver(nil)

	dst := make([]byte, 640)
	for step := 0; step < n; step++ {
		// Filler, so a render the layer declines to do (row past the current
		// LineHeight) shows up as filler rather than smearing the last row.
		for i := range dst {
			dst[i] = 0xEE
		}
		w := l.LineWidth()
		l.RenderScanline(step*7%l.LineHeight(), dst)
		out = append(out, byte(w>>8), byte(w))
		out = append(out, dst[:w]...)

		l.SetScrollX(l.ScrollX() + 5)
		l.SetScrollY(l.ScrollY() + 3)
		if step%6 == 5 { // the double-buffer flip
			a, s := l.ActiveBank(), l.ShadowBank()
			l.SetActiveBank(s)
			l.SetShadowBank(a)
		}
		if step%7 == 6 {
			l.SetPaletteOffset(l.PaletteOffset() + 1)
		}
		if step%9 == 8 {
			l.SetResolution((l.Resolution() + 1) % 3)
		}
		if step%11 == 10 {
			l.SetEnabled(!l.Enabled())
		}
	}
	return out
}

// primed returns a Layer 2 stopped MID-FRAME: thirteen lines into the per-line
// sequence above, so its scroll pair, banks, resolution and palette offset all
// hold values that only the sequence so far explains.
func primed() *Layer2 {
	l := New(newFakeBanks())
	l.SetEnabled(true)
	l.SetResolution(1) // 320x256: the wide, column-major address path
	l.SetPaletteOffset(5)
	l.SetActiveBank(9)
	l.SetShadowBank(40)
	l.SetScrollX(0x137) // 9-bit, past the 256-column wrap
	l.SetScrollY(0xC1)
	drive(l, 13)
	return l
}

// The property that matters: from a captured state, the layer must draw what
// it drew the first time.
func TestReplayingFromCapturedStateReproducesTheSameFrames(t *testing.T) {
	l := primed()

	st := l.SaveState()
	first := drive(l, 20)

	if err := l.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(l, 20)

	if !bytes.Equal(first, second) {
		t.Error("Layer 2 rendered differently on replay from the same captured state: " +
			"some part of the layer is not being captured")
	}
}

// The negative that gives the test above its teeth. If the drive sequence did
// not actually move the layer off the state it was captured in, every field
// would round-trip for free and the replay would pass while capturing nothing.
func TestTheDriveSequenceLeavesEveryCapturedFieldChanged(t *testing.T) {
	l := primed()
	before := struct {
		active, shadow, res, off, sy byte
		sx                           uint16
		on                           bool
	}{l.ActiveBank(), l.ShadowBank(), l.Resolution(), l.PaletteOffset(), l.ScrollY(), l.ScrollX(), l.Enabled()}

	drive(l, 20)

	if l.ActiveBank() == before.active {
		t.Error("active bank unchanged by the drive: a lost ActiveBank restore could not be detected")
	}
	if l.ShadowBank() == before.shadow {
		t.Error("shadow bank unchanged by the drive: a lost ShadowBank restore could not be detected")
	}
	if l.Resolution() == before.res {
		t.Error("resolution unchanged by the drive")
	}
	if l.PaletteOffset() == before.off {
		t.Error("palette offset unchanged by the drive")
	}
	if l.ScrollX() == before.sx {
		t.Error("scroll X unchanged by the drive")
	}
	if l.ScrollY() == before.sy {
		t.Error("scroll Y unchanged by the drive")
	}
	if l.Enabled() == before.on {
		t.Error("enable unchanged by the drive: a lost Enabled restore could not be detected")
	}
}

// A restore is not a guest write. The scroll observer is the raster journal's
// hook (cmd/zx_go/next.go wires it so a mid-frame scroll write takes effect
// from its own scanline); firing it while rewinding would push a scroll change
// into a journal that is being rewound itself.
func TestRestoreDoesNotReportAScrollWrite(t *testing.T) {
	l := primed()
	st := l.SaveState()
	l.SetScrollX(l.ScrollX() + 64)
	l.SetScrollY(l.ScrollY() + 7)

	var seen int
	l.SetScrollObserver(func(ScrollWrite) { seen++ })
	if err := l.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	l.SetScrollObserver(nil)

	if seen != 0 {
		t.Errorf("restore reported %d scroll write(s); a rewind must not look like a guest write", seen)
	}
	if l.ScrollX() != 0x137+13*5 || l.ScrollY() != 0xC1+13*3 {
		t.Errorf("scroll = %#x/%#x after restore, want the captured pair", l.ScrollX(), l.ScrollY())
	}
}

// A capture must not alias the layer, in either direction: later changes must
// not reach back into a blob already handed out, and a restored layer must not
// keep reading the caller's buffer.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	l := primed()
	st := l.SaveState()
	keep := append([]byte(nil), st...)

	drive(l, 20)
	if l.SaveState() == nil {
		t.Fatal("SaveState returned nil")
	}
	if !bytes.Equal(st, keep) {
		t.Error("a later capture rewrote the blob handed out by an earlier one")
	}

	if err := l.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	want := drive(l, 20)

	// Scribble the caller's buffer: a layer that retained it would now be
	// restoring from rubbish.
	if err := l.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i := range st {
		st[i] = 0xFF
	}
	if got := drive(l, 20); !bytes.Equal(got, want) {
		t.Error("the layer kept a reference to the caller's state buffer")
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New(nil).StateID(); got != "next.layer2" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.layer2")
	}
}

// A malformed blob must be reported and must leave the layer exactly as it
// was: half-applied state is the failure a rewind cannot recover from, because
// the machine still runs.
func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	l := primed()
	ref := primed() // an untouched twin, driven the same way

	if err := l.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if err := l.LoadState(nil); err == nil {
		t.Error("an empty state blob must be reported")
	}

	if !bytes.Equal(drive(l, 20), drive(ref, 20)) {
		t.Error("a rejected state blob still changed the layer")
	}
}
