package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// The bottom two character rows are the ROM's editor and report area, not the
// program's canvas. A screen holding nothing but a BASIC cursor there is an
// idle ROM prompt, and recording it as a title that drew something is how
// Adidas Championship Tie-Break scored 181 pixels while running nothing.
func TestClassifyEditorLineIsNotContent(t *testing.T) {
	s := Screening{Pixels: 181, Colours: 2, CanvasPixels: 0, CanvasColours: 1}
	if got := Classify(s); got != VerdictBlank {
		t.Errorf("Classify(%+v) = %v, want VerdictBlank", s, got)
	}
}

// A one-line prompt the game itself drew is content, however few pixels it
// lights. Alien Syndrome's "PRESS ENTER" is 498 pixels and the game plays.
func TestClassifySparseCanvasIsContent(t *testing.T) {
	for _, s := range []Screening{
		{Pixels: 498, Colours: 2, CanvasPixels: 498, CanvasColours: 2}, // Alien Syndrome
		{Pixels: 845, Colours: 2, CanvasPixels: 845, CanvasColours: 2}, // Capitan Sevilla
	} {
		if got := Classify(s); got == VerdictBlank {
			t.Errorf("Classify(%+v) = VerdictBlank, want content", s)
		}
	}
}

// The canvas rule may only ever rescue a Blank. If it could push a title the
// other way it would be a regression risk for every row already recorded.
func TestCanvasRuleOnlyEverRescuesBlank(t *testing.T) {
	base := Screening{Pixels: 20000, Colours: 8, Moved: true}
	withCanvas := base
	withCanvas.CanvasPixels, withCanvas.CanvasColours = 0, 1
	if Classify(base) != Classify(withCanvas) {
		t.Errorf("canvas fields changed a non-blank verdict: %v vs %v",
			Classify(base), Classify(withCanvas))
	}
}

func TestDropBottomLinesRemovesTheEditorArea(t *testing.T) {
	// A display window painted flat, with content only in the editor area.
	pix := make([]byte, ula.ScreenWidth*ula.ScreenHeight*4)
	for i := range pix {
		pix[i] = 0xC0
	}
	first := (ula.ScreenHeight - editorLines) * ula.ScreenWidth * 4
	for i := first; i < len(pix); i += 4 {
		pix[i] = 0x00
	}

	if p, _ := measureFrame(pix); p == 0 {
		t.Fatal("test setup drew nothing in the editor area")
	}
	if p, _ := measureFrame(dropBottomLines(pix, editorLines)); p != 0 {
		t.Errorf("canvas measured %d pixels, want 0 — editor area was not dropped", p)
	}
}

func TestDropBottomLinesKeepsTheCanvas(t *testing.T) {
	pix := make([]byte, ula.ScreenWidth*ula.ScreenHeight*4)
	for i := range pix {
		pix[i] = 0xC0
	}
	// One character row of content in the middle of the screen.
	start := 100 * ula.ScreenWidth * 4
	for i := start; i < start+8*ula.ScreenWidth*4; i += 4 {
		pix[i] = 0x00
	}

	p, _ := measureFrame(dropBottomLines(pix, editorLines))
	if want := 8 * ula.ScreenWidth; p != want {
		t.Errorf("canvas measured %d pixels, want %d", p, want)
	}
}
