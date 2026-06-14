package sprite

import "testing"

func TestEngineDefaults(t *testing.T) {
	e := New()
	if e.Enabled() {
		t.Errorf("default Enabled = true, want false")
	}
	if e.SelectedSprite() != 0 {
		t.Errorf("default SelectedSprite = %d, want 0", e.SelectedSprite())
	}
}

func TestSelectSpriteMasksHighBit(t *testing.T) {
	e := New()
	e.SelectSprite(0xFF)
	if e.SelectedSprite() != 0x7F {
		t.Errorf("SelectSprite(0xFF): SelectedSprite = %#x, want 0x7F", e.SelectedSprite())
	}
}

func TestSpritePatternWriteRoundTrip(t *testing.T) {
	e := New()
	e.SetPatternAddr(0x100)
	e.WritePatternByte(0xAB)
	e.WritePatternByte(0xCD)
	if e.PatternByte(0x100) != 0xAB || e.PatternByte(0x101) != 0xCD {
		t.Errorf("pattern bytes = %#x %#x, want 0xAB 0xCD", e.PatternByte(0x100), e.PatternByte(0x101))
	}
}

// Sprite engine OOB + auto-wrap coverage (iter 264).

func TestSetPatternAddr_ClampsToMax(t *testing.T) {
	e := New()
	// Pass an address well beyond PatternRAMSize — implementation
	// clamps to (size - 1).
	e.SetPatternAddr(0xFFFF)
	// Now writing one byte advances cursor; reading at the clamped
	// position should produce the byte we just wrote.
	e.WritePatternByte(0x77)
	if got := e.PatternByte(PatternRAMSize - 1); got != 0x77 {
		t.Errorf("clamped write: PatternByte(max-1) = %02X, want 77", got)
	}
}

func TestWritePatternByte_WrapsAroundCursor(t *testing.T) {
	e := New()
	// Set cursor near the end so the next write triggers wrap.
	e.SetPatternAddr(uint16(PatternRAMSize - 1))
	e.WritePatternByte(0xAA) // lands at last position
	e.WritePatternByte(0xBB) // should wrap to position 0
	if got := e.PatternByte(uint16(PatternRAMSize - 1)); got != 0xAA {
		t.Errorf("at max-1: %02X, want AA", got)
	}
	if got := e.PatternByte(0); got != 0xBB {
		t.Errorf("wrap to 0: %02X, want BB", got)
	}
}

func TestPatternByte_OOBReturnsZero(t *testing.T) {
	e := New()
	// PatternByte addr beyond size → 0.
	if got := e.PatternByte(uint16(PatternRAMSize)); got != 0 {
		t.Errorf("PatternByte(size) = %02X, want 0", got)
	}
}

func TestSpriteSet_OOBSilentlyIgnored(t *testing.T) {
	e := New()
	// Set + Sprite on out-of-range index — must not panic, returns nil/no-op.
	e.Set(MaxSprites, Attr{X: 100, Y: 50, Visible: true})
	if e.Sprite(MaxSprites) != nil {
		t.Errorf("Sprite(MaxSprites) = non-nil")
	}
	if e.Sprite(-1) != nil {
		t.Errorf("Sprite(-1) = non-nil")
	}
	e.Set(-1, Attr{Visible: true}) // must not panic
}

func TestSelectedSpriteRoundTrip(t *testing.T) {
	e := New()
	if got := e.SelectedSprite(); got != 0 {
		t.Errorf("default SelectedSprite = %d, want 0", got)
	}
	e.SelectSprite(0x3F)
	if got := e.SelectedSprite(); got != 0x3F {
		t.Errorf("after Select($3F): SelectedSprite = %d", got)
	}
	// High bit is masked.
	e.SelectSprite(0xFF)
	if got := e.SelectedSprite(); got != 0x7F {
		t.Errorf("after Select($FF): SelectedSprite = %d, want $7F (high bit masked)", got)
	}
}

func TestRenderScanlineSimpleSprite(t *testing.T) {
	// One sprite, all-1 pattern, palette offset 0, placed at (10, 20).
	// Row 20 should have pixels 10..25 set to palette index 1.
	e := New()
	e.SetEnabled(true)

	// 4bpp pattern: each byte = 2 pixels (high nibble, low nibble).
	// 16 rows of 8 bytes each = 128 bytes per pattern.
	// Pattern index 0 starts at byte 0.
	for i := 0; i < 128; i++ {
		// Each byte = 0x11 means both pixels are index 1.
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	e.Set(0, Attr{X: 10, Y: 20, Pattern: 0, Palette: 0, Visible: true})

	dst := make([]byte, 256)
	e.RenderScanline(20, dst, 256)
	for x := 10; x < 26; x++ {
		if dst[x] != 1 {
			t.Errorf("dst[%d] = %d, want 1", x, dst[x])
		}
	}
	// Outside the sprite -> unchanged (still 0)
	for x := 0; x < 10; x++ {
		if dst[x] != 0 {
			t.Errorf("dst[%d] before sprite = %d, want 0", x, dst[x])
			break
		}
	}
	for x := 26; x < 256; x++ {
		if dst[x] != 0 {
			t.Errorf("dst[%d] after sprite = %d, want 0", x, dst[x])
			break
		}
	}
}

func TestRenderScanlineTransparencyIsIndexZero(t *testing.T) {
	// Pattern of all-0 nibbles = fully transparent.
	e := New()
	e.SetEnabled(true)
	// All zeros (default); just place a sprite.
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 0, Palette: 5, Visible: true})

	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 0xAA
	}
	e.RenderScanline(0, dst, 256)
	for x := 0; x < 16; x++ {
		if dst[x] != 0xAA {
			t.Errorf("dst[%d] = %#x; transparent pixels should not overwrite", x, dst[x])
			break
		}
	}
}

func TestRenderScanlineDisabledIsNoOp(t *testing.T) {
	e := New()
	// Not enabled.
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 0, Visible: true})

	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 0xBB
	}
	e.RenderScanline(0, dst, 256)
	for x := 0; x < 16; x++ {
		if dst[x] != 0xBB {
			t.Errorf("dst[%d] disturbed while engine disabled", x)
			break
		}
	}
}

func TestRenderScanlineClipsOffscreen(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	// Negative X: sprite half off-screen left.
	e.Set(0, Attr{X: -8, Y: 0, Pattern: 0, Visible: true})

	dst := make([]byte, 256)
	e.RenderScanline(0, dst, 256)
	// Pixels 0..7 should be set (right half of sprite).
	for x := 0; x < 8; x++ {
		if dst[x] != 1 {
			t.Errorf("dst[%d] = %d, want 1 (right half of clipped sprite)", x, dst[x])
		}
	}
	// Past the sprite — no pixels.
	if dst[8] != 0 {
		t.Errorf("dst[8] = %d, want 0", dst[8])
	}
}

func TestPaletteOffsetIsApplied(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	// Palette is the 4-bit offset (NR$37 bits 7:4); it becomes the
	// index's HIGH nibble per FPGA sprites.vhd:968: (offset<<4)|pixel.
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 0, Palette: 0x02, Visible: true})

	dst := make([]byte, 256)
	e.RenderScanline(0, dst, 256)
	if dst[0] != 0x21 {
		t.Errorf("palette-offset pixel = %#x, want 0x21 ((offset 2 << 4)|index 1)", dst[0])
	}
}

func TestSpriteOutOfRange(t *testing.T) {
	e := New()
	if e.Sprite(-1) != nil || e.Sprite(MaxSprites) != nil {
		t.Errorf("Sprite out-of-range should return nil")
	}
	// Set out-of-range is a silent no-op.
	e.Set(-1, Attr{Visible: true})
	e.Set(MaxSprites, Attr{Visible: true})
}

// ========================================================================
// Sprite render edge tests (iter 209).
// ========================================================================

// TestRenderScanline_X9HighBit verifies that a sprite at X >= 256
// (= X9 set, the high bit of the 9-bit X coordinate) renders into
// the right-hand portion of the 320-pixel canvas.
func TestRenderScanline_X9HighBit(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	// X = 260 → X9=1, low 8 = 4. Sprite occupies pixels 260..275.
	e.Set(0, Attr{X: 260, Y: 50, Pattern: 0, Palette: 0, Visible: true})

	dst := make([]byte, 320)
	e.RenderScanline(50, dst, 320)
	// Pixels 0..259 untouched.
	for i := 0; i < 260; i++ {
		if dst[i] != 0 {
			t.Errorf("pre-sprite pixel %d = $%02X, want 0", i, dst[i])
			break
		}
	}
	// Pixels 260..275 = palette index 1.
	for i := 260; i < 260+16; i++ {
		if dst[i] != 1 {
			t.Errorf("sprite pixel %d = $%02X, want 1 (X9 sprite)", i, dst[i])
			break
		}
	}
}

// TestRenderScanline_VerticalBoundary verifies a sprite at Y=0 lights
// up rows 0..15 only, and the lines above (negative Y in display
// space — none here) plus row 16 are untouched.
func TestRenderScanline_VerticalBoundary(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	e.Set(0, Attr{X: 50, Y: 0, Pattern: 0, Palette: 0, Visible: true})

	// Row 0 — should have sprite.
	dst := make([]byte, 256)
	e.RenderScanline(0, dst, 256)
	if dst[50] != 1 {
		t.Errorf("row 0 col 50: $%02X, want 1 (sprite at Y=0)", dst[50])
	}

	// Row 15 — should still have sprite (last visible row).
	dst = make([]byte, 256)
	e.RenderScanline(15, dst, 256)
	if dst[50] != 1 {
		t.Errorf("row 15: $%02X, want 1 (last sprite row)", dst[50])
	}

	// Row 16 — sprite gone.
	dst = make([]byte, 256)
	e.RenderScanline(16, dst, 256)
	if dst[50] != 0 {
		t.Errorf("row 16: $%02X, want 0 (past sprite)", dst[50])
	}
}

// TestRenderScanline_MultipleSpritesOverlap verifies that the second-
// numbered sprite paints over the first when they share pixels.
// Per the spec, sprite 0 is "lowest priority" — sprite N+1 is drawn
// after sprite N and therefore wins at overlap.
func TestRenderScanline_MultipleSpritesOverlap(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	// Pattern 0 = all 1s; pattern 1 = all 2s.
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(128 + i))
		e.WritePatternByte(0x22)
	}
	// Both at same position — sprite 1 (pattern 1, all $22) should win.
	e.Set(0, Attr{X: 30, Y: 30, Pattern: 0, Palette: 0, Visible: true})
	e.Set(1, Attr{X: 30, Y: 30, Pattern: 1, Palette: 0, Visible: true})

	dst := make([]byte, 256)
	e.RenderScanline(30, dst, 256)
	if dst[30] != 2 {
		t.Errorf("overlap pixel: $%02X, want 2 (sprite 1 wins as higher-numbered)",
			dst[30])
	}
}

// TestRenderScanline_OffscreenRightClipped verifies a sprite extending
// past width is clipped without panic.
func TestRenderScanline_OffscreenRightClipped(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	// X = 250 in a 256-wide buffer — pixels 250..255 fit, 256..265
	// would overflow.
	e.Set(0, Attr{X: 250, Y: 10, Pattern: 0, Palette: 0, Visible: true})

	dst := make([]byte, 256)
	e.RenderScanline(10, dst, 256)
	for i := 250; i < 256; i++ {
		if dst[i] != 1 {
			t.Errorf("on-screen pixel %d = $%02X, want 1", i, dst[i])
		}
	}
	// Beyond width: no buffer overrun (test would panic if it happened).
}

// TestRenderScanline_InvisibleSpriteSkipped — Attr.Visible=false
// means the sprite must not contribute pixels.
func TestRenderScanline_InvisibleSpriteSkipped(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	e.Set(0, Attr{X: 50, Y: 50, Pattern: 0, Palette: 0, Visible: false})

	dst := make([]byte, 256)
	e.RenderScanline(50, dst, 256)
	if dst[50] != 0 {
		t.Errorf("invisible sprite painted: dst[50] = $%02X", dst[50])
	}
}
