package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The screening's content tests are written against the ULA display file,
// which is not what a Next draws with. Every time one of them is tightened,
// the Next is the machine it can quietly break: DisplayFile still hands back a
// 6912-byte buffer for it, so a test that reads that buffer gets an answer
// rather than an error, and the answer is meaningless.
//
// These pin the two ways that has already happened.

// TestFlatBitmapDoesNotCondemnNextVideoModes is the first: a Layer 2 or
// tilemap title draws through its own hardware and may never touch the ULA
// bitmap, so asking "is that bitmap flat" says nothing about the picture. It
// condemned both vendored Next corpus titles, which is the error ScreenTitle's
// own doc calls the worst this harness can make.
func TestFlatBitmapDoesNotCondemnNextVideoModes(t *testing.T) {
	for _, f := range []string{
		"testdata/corpus/bin/zxnext_tilemap.nex",
		"testdata/corpus/bin/zxnext_layer2_tilemap.nex",
	} {
		h, err := New(roms.ModelNext)
		if err != nil {
			t.Skipf("Next unavailable: %v", err)
		}
		s, err := h.ScreenFile(f, 200)
		if err != nil {
			h.CloseFiles()
			t.Fatalf("%s: %v", f, err)
		}
		if s.FlatBitmap {
			t.Errorf("%s: FlatBitmap = true on a machine whose picture is not the ULA bitmap", f)
		}
		if v := Classify(s); v == VerdictBlank {
			t.Errorf("%s: Classify = Blank for %d px in %d colours", f, s.Pixels, s.Colours)
		}
		h.CloseFiles()
	}
}

// TestScreenFileStillLoadsANextTape is the second: the harness boots the Next
// to 48 BASIC on the embedded ROM precisely so tape-loaded conformance
// programs run without the proprietary OS, and the corpus ships one. Routing
// the tape branch through StartTapeLoad turned every Next tape row into a load
// error, because that function's default arm refuses the Next.
func TestScreenFileStillLoadsANextTape(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Skipf("Next unavailable: %v", err)
	}
	defer h.CloseFiles()
	if _, err := h.ScreenFile("testdata/corpus/bin/zilogdma.tap", 600); err != nil {
		t.Errorf("ScreenFile on a Next tape: %v", err)
	}
}
