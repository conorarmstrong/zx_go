package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A screen can clear both content floors and still be nothing at all.
//
// Eight +3 disk rows in docs/compatibility.md were recorded as **Boots** —
// "the guest's own code ran and drew its title" — on an identical measurement
// of 18432 px in 2 colours. Five unrelated games sharing a measurement to the
// pixel is not a coincidence, and the picture settles it: a uniform vertical
// stripe pattern, which is what uninitialised video memory looks like. Every
// bitmap byte is the same value and only the attributes vary, so the pixel
// count is high, the colour count is 2, and nothing was drawn.
//
// The differential tool has always caught these — its blank test asks whether
// the bitmap has any structure at all, rather than counting lit pixels — and
// it reported all eight as BLANK. This brings the same test to the screening.

// flatBitmapScreen fills the display file with one repeated bitmap byte over a
// uniform attribute, which is the shape of the frames those rows were scored
// on: 18432 px in exactly 2 colours.
//
// The colour count is load-bearing. The veto is bounded by it so that artwork
// painted in the attribute bytes over a solid bitmap — a real screen, and a
// many-coloured one — is not condemned along with uninitialised memory.
func flatBitmapScreen(h *Harness) {
	for a := 0x4000; a < 0x5800; a++ {
		h.WriteMemory(uint16(a), 0x55)
	}
	for a := 0x5800; a < 0x5B00; a++ {
		h.WriteMemory(uint16(a), 0x28) // green paper, black ink
	}
}

// TestScreenTitleReportsAFlatBitmapAsNoContent is the fault: the frame clears
// MinContentPixels and MinContentColours comfortably, and there is nothing on
// it.
func TestScreenTitleReportsAFlatBitmapAsNoContent(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	t.Cleanup(h.CloseFiles)
	h.RunFrames(200) // reach the prompt, then paint over it
	flatBitmapScreen(h)
	h.RunFrames(1)

	s := h.ScreenTitle(0)

	// The premise: this is exactly the kind of frame the floors let through.
	if s.Pixels < MinContentPixels || s.Colours < MinContentColours {
		t.Fatalf("frame measured %d px in %d colours, which the floors already reject — "+
			"the fixture is not reproducing the case", s.Pixels, s.Colours)
	}
	if !s.FlatBitmap {
		t.Errorf("FlatBitmap = false for a display file whose every bitmap byte is $55")
	}
	if got := Classify(s); got != VerdictBlank {
		t.Errorf("Classify = %v for a flat bitmap measuring %d px in %d colours, want %v",
			got, s.Pixels, s.Colours, VerdictBlank)
	}
}

// The veto must not condemn a picture painted in the attribute bytes over a
// solid bitmap. That is real artwork, it is common on the Spectrum, and its
// bitmap is every bit as flat as uninitialised memory's — the colour count is
// what separates them.
func TestClassifyKeepsAttributeArtworkOverAFlatBitmap(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	t.Cleanup(h.CloseFiles)
	h.RunFrames(200)
	for a := 0x4000; a < 0x5800; a++ {
		h.WriteMemory(uint16(a), 0xFF) // solid: every pixel is ink
	}
	// A different colour per cell is what makes this a picture rather than a
	// rectangle, and it is the thing the pixel count alone cannot see.
	for a := 0x5800; a < 0x5B00; a++ {
		h.WriteMemory(uint16(a), byte(a%64))
	}
	h.RunFrames(1)

	s := h.ScreenTitle(0)

	if !s.FlatBitmap {
		t.Fatalf("FlatBitmap = false, so this test is not exercising the case it exists for")
	}
	if got := Classify(s); got == VerdictBlank {
		t.Errorf("Classify = Blank for attribute artwork measuring %d px in %d colours",
			s.Pixels, s.Colours)
	}
}

// The guard that keeps the new test from condemning working titles: a real
// screen has structure in its bitmap, so it must be unaffected.
func TestScreenTitleDoesNotCallARealScreenFlat(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	t.Cleanup(h.CloseFiles)
	h.RunFrames(200)
	// One byte different from its neighbours is enough to be structure — the
	// test is "is anything drawn at all", not "is much drawn".
	for a := 0x4000; a < 0x5800; a++ {
		h.WriteMemory(uint16(a), 0x55)
	}
	h.WriteMemory(0x4820, 0x3C)
	h.RunFrames(1)

	if s := h.ScreenTitle(0); s.FlatBitmap {
		t.Errorf("FlatBitmap = true for a bitmap that is not uniform")
	}
}
