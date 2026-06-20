package sam

import (
	"image/color"
	"testing"
)

func TestRenderMode2LineAttributes(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x20) // mode 2
	m.clut[0] = 0       // paper black
	m.clut[1] = 127     // ink white
	// MODE 2 data is linear (line<<5)+cell; attributes are per-line at 0x2000.
	// Line 1, cell 0: top pixel ink. Attr for line 1 cell 0 → ink=1, paper=0.
	writeDisplay(m, (1<<5)+0, 0x80)
	writeDisplay(m, mode2AttrOffset+(1<<5)+0, 0x01)

	img := m.renderAll()
	if got := activeAt(img, 0, 1); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("mode 2 line 1 pixel 0 should be ink (white), got %v", got)
	}
	if got := activeAt(img, 2, 1); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("mode 2 line 1 pixel 1 should be paper (black), got %v", got)
	}
}

func TestRenderMode3SubCLUTSwap(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x40) // mode 3
	m.Mem.SetHMPR(0x00) // mode-3 colour base = 0 → CLUT entries 0..3
	// Distinct CLUT entries so the 1↔2 swap is observable.
	m.clut[0] = 0    // black
	m.clut[1] = 0x70 // -> sub-entry 2 (swapped)
	m.clut[2] = 0x07 // -> sub-entry 1 (swapped)
	m.clut[3] = 127  // white
	// One byte = pixels 0,1,2,3 (2bpp). 0b00_01_10_11 = 0x1B.
	writeDisplay(m, 0, 0x1B)

	img := m.renderAll()
	// pixel value 1 → sub-entry 1 → clut[2] (swap), pixel value 2 → clut[1].
	if got := activeAt(img, 1, 0); got != PaletteColour(0x07) {
		t.Errorf("mode 3 pixel value 1 should map (swapped) to clut[2]=0x07, got %v", got)
	}
	if got := activeAt(img, 2, 0); got != PaletteColour(0x70) {
		t.Errorf("mode 3 pixel value 2 should map (swapped) to clut[1]=0x70, got %v", got)
	}
	if got := activeAt(img, 3, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("mode 3 pixel value 3 should be clut[3]=white, got %v", got)
	}
}

// TestLineAccurateSplit proves the lazy renderer draws lines with the state that
// was active when the raster passed them: a CLUT change part-way down the frame
// affects only the lower lines.
func TestLineAccurateSplit(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00) // mode 1
	for off := 0; off < mode12DataBytes; off++ {
		writeDisplay(m, off, 0xFF) // all-ink pixels
	}
	for off := mode12DataBytes; off < mode12DataBytes+768; off++ {
		writeDisplay(m, off, 0x01) // attr ink=1, paper=0
	}

	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}

	m.renderCursor = 0
	m.clut[1] = 0 // ink black for the top half
	m.flushTo(samBorderTop + 96)
	m.clut[1] = 127 // ink white for the bottom half
	m.flushTo(samTotalHeight)

	if got := activeAt(m.frame, 0, 10); got != black {
		t.Errorf("top half should use the old CLUT (black ink), got %v", got)
	}
	if got := activeAt(m.frame, 0, 150); got != white {
		t.Errorf("bottom half should use the new CLUT (white ink), got %v", got)
	}
}

func TestSOFFBlanksMode4(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x60) // mode 4
	m.clut[15] = 127    // would render white...
	writeDisplay(m, 0, 0xFF)
	m.border = borderSOFF // ...but SOFF blanks modes 3/4
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("SOFF should blank mode 4 to black, got %v", got)
	}
	// SOFF must NOT blank mode 1 (it only affects modes 3/4).
	m.Mem.SetVMPR(0x00)
	m.clut[1] = 127
	writeDisplay(m, 0, 0x80)
	writeDisplay(m, mode12DataBytes, 0x01)
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("SOFF should not blank mode 1, got %v", got)
	}
}
