package sam

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

// activeAt reads a pixel in active-screen coordinates (0,0 = top-left of the
// 512×192 display area inside the border).
func activeAt(img *image.RGBA, x, y int) color.RGBA {
	return img.RGBAAt(x+samBorderLeft, y+samBorderTop)
}

func TestPaletteSpotValues(t *testing.T) {
	black := PaletteColour(0)
	if black.R != 0 || black.G != 0 || black.B != 0 {
		t.Errorf("index 0 should be black, got %v", black)
	}
	white := PaletteColour(127)
	if white.R != 255 || white.G != 255 || white.B != 255 {
		t.Errorf("index 127 should be white, got %v", white)
	}
	// Index 0x11 = bits 0 and 4 → pure blue 6/7, no red/green.
	blue := PaletteColour(0x11)
	if blue.R != 0 || blue.G != 0 || blue.B != uint8(6*255/7) {
		t.Errorf("index 0x11 should be pure blue 6/7, got %v", blue)
	}
}

// writeDisplay sets a byte in the display page (page 0 at reset / VMPR 0).
func writeDisplay(m *Machine, off int, val byte) { m.Mem.ram[off/PageSize][off%PageSize] = val }

func TestRenderMode1(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00) // mode 1, page 0
	m.clut[0] = 0       // paper → palette black
	m.clut[1] = 127     // ink   → palette white
	// Cell (0,0): top pixel ink, rest paper; attr ink=1 paper=0, no flash.
	writeDisplay(m, 0, 0x80)      // line 0, cell 0 pixel data
	writeDisplay(m, 6144+0, 0x01) // attr: paper=0, ink=1

	img := m.renderAll()
	if got := activeAt(img, 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("pixel 0 should be ink (white), got %v", got)
	}
	if got := activeAt(img, 2, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("pixel 1 should be paper (black), got %v", got)
	}
}

func TestRenderMode4(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x60)      // mode 4, page 0
	m.clut[1] = 127          // hi nibble → white
	m.clut[2] = 0x11         // lo nibble → blue
	writeDisplay(m, 0, 0x12) // byte 0: hi=1, lo=2

	img := m.renderAll()
	if got := activeAt(img, 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("hi nibble pixel should be white, got %v", got)
	}
	if got := activeAt(img, 2, 0); got != PaletteColour(0x11) {
		t.Errorf("lo nibble pixel should be blue, got %v", got)
	}
}

func TestRenderMode1Flash(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.Mem.SetVMPR(0x00)
	m.clut[0] = 0                 // paper black
	m.clut[1] = 127               // ink white
	writeDisplay(m, 0, 0x80)      // pixel 0 = ink bit
	writeDisplay(m, 6144+0, 0x81) // attr: ink=1, paper=0, FLASH (bit7)

	m.frameCount = 0 // flash phase off → ink shows as ink (white)
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("flash off: pixel should be ink (white), got %v", got)
	}
	m.frameCount = flashFramesPhase // flash phase on → ink/paper swapped
	if got := activeAt(m.renderAll(), 0, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("flash on: pixel should be swapped to paper (black), got %v", got)
	}
}

// TestRenderBootScreen boots the real ROM and renders the screen, saving a PNG
// for eyeballing. Asserts the result isn't a uniform blank (the BASIC screen
// has structure).
func TestRenderBootScreen(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ { // enough frames for the (contended) boot to draw
		m.RunFrame()
	}
	t.Logf("boot screen mode = %d, VMPR=%#02x, border=%#x", m.Mem.ScreenMode(), m.Mem.VMPR(), m.BorderColour())

	img := m.Render() // the frame built line-accurately during RunFrame
	if out := os.Getenv("SAM_RENDER_OUT"); out != "" {
		f, _ := os.Create(out)
		_ = png.Encode(f, img)
		_ = f.Close()
		t.Logf("wrote %s", out)
	}
	first := activeAt(img, 0, 0)
	uniform := true
	for y := 0; y < samActiveHeight && uniform; y++ {
		for x := 0; x < samActiveWidth; x++ {
			if activeAt(img, x, y) != first {
				uniform = false
				break
			}
		}
	}
	if uniform {
		t.Error("rendered boot screen is uniform/blank — expected structure")
	}
}
