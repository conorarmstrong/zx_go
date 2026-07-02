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

// Sprite engine out-of-bounds + pattern-cursor auto-wrap coverage.

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
	e.Set(0, Attr{X: 10, Y: 20, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

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
	e.Set(0, Attr{X: -8, Y: 0, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})

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
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 0, Palette: 0x02, Visible: true, Extended: true, Byte4: 0x80})

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
// Sprite render edge tests.
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
	e.Set(0, Attr{X: 260, Y: 50, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

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
	e.Set(0, Attr{X: 50, Y: 0, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

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
	// Pattern slot 0 = all 1s; pattern slot 1 = all 2s. Slots are 256 bytes
	// (slot 0 @ addr 0, slot 1 @ addr 256); a 4bpp sprite reads the low half.
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(256 + i))
		e.WritePatternByte(0x22)
	}
	// Both at same position — sprite 1 (pattern 1, all $22) should win.
	e.Set(0, Attr{X: 30, Y: 30, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})
	e.Set(1, Attr{X: 30, Y: 30, Pattern: 1, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

	dst := make([]byte, 256)
	e.RenderScanline(30, dst, 256)
	if dst[30] != 2 {
		t.Errorf("overlap pixel: $%02X, want 2 (sprite 1 wins as higher-numbered)",
			dst[30])
	}
}

// TestSpriteOverBorderClip covers NextReg $15 bit 1 ("sprites over border")
// and bit 5 ("border clip enable"), per sprites.vhd:1043-1066. When
// over-border is OFF, the FPGA shifts the sprite clip window into the central
// paper area (clip_y1=0 -> y_s=32), so a sprite at Y<32 (the top-border band of
// the 320x256 frame) is NOT drawn, while a sprite at Y=205 (inside [32,223])
// stays visible.
func TestSpriteOverBorderClip(t *testing.T) {
	mk := func() *Engine {
		e := New()
		e.SetEnabled(true)
		for i := 0; i < 128; i++ {
			e.SetPatternAddr(uint16(i))
			e.WritePatternByte(0x11) // slot 0 = all 1s
		}
		e.SetClip(0, 255, 0, 191) // FPGA reset clip window
		return e
	}
	renderAt := func(e *Engine, y int) byte {
		dst := make([]byte, 320)
		e.RenderScanline(y, dst, 320)
		return dst[40]
	}

	// over-border OFF (default): a sprite at Y=1 (border) is clipped...
	e := mk()
	e.SetOverBorder(false)
	e.Set(0, Attr{X: 40, Y: 1, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})
	if got := renderAt(e, 8); got != 0 {
		t.Errorf("over-border off, Y=1 sprite at scanline 8: $%02X, want 0 (clipped to paper)", got)
	}
	// ...but a sprite in the paper (Y=40) draws, and so does Y=205 (the lives).
	e.Set(0, Attr{X: 40, Y: 40, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})
	if got := renderAt(e, 45); got != 1 {
		t.Errorf("over-border off, Y=40 sprite at scanline 45: $%02X, want 1 (inside paper)", got)
	}
	e.Set(0, Attr{X: 40, Y: 205, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})
	if got := renderAt(e, 210); got != 1 {
		t.Errorf("over-border off, Y=205 sprite at scanline 210: $%02X, want 1 (lives, inside [32,223])", got)
	}

	// over-border ON + border-clip OFF: no clipping, the Y=1 sprite draws.
	e2 := mk()
	e2.SetOverBorder(true)
	e2.SetBorderClip(false)
	e2.Set(0, Attr{X: 40, Y: 1, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})
	if got := renderAt(e2, 8); got != 1 {
		t.Errorf("over-border on, Y=1 sprite at scanline 8: $%02X, want 1 (over border)", got)
	}
}

// TestRenderScanline_ZeroOnTopPriority covers NextReg $15 bit 6
// ("sprite_priority" / zero-on-top, sprites.vhd:73). When enabled, the
// LOWEST-numbered sprite is drawn in front (sprite 0 on top) instead of the
// default highest-numbered.
func TestRenderScanline_ZeroOnTopPriority(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11) // slot 0 = all 1s
	}
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(256 + i))
		e.WritePatternByte(0x22) // slot 1 = all 2s
	}
	e.Set(0, Attr{X: 30, Y: 30, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})
	e.Set(1, Attr{X: 30, Y: 30, Pattern: 1, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

	// zero-on-top ENABLED: sprite 0 (all 1s) must win.
	e.SetZeroOnTop(true)
	dst := make([]byte, 256)
	e.RenderScanline(30, dst, 256)
	if dst[30] != 1 {
		t.Errorf("zero-on-top overlap: $%02X, want 1 (sprite 0 wins, lowest-numbered on top)", dst[30])
	}

	// Disabling it restores the default highest-numbered-on-top behaviour.
	e.SetZeroOnTop(false)
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(30, dst, 256)
	if dst[30] != 2 {
		t.Errorf("zero-on-top off overlap: $%02X, want 2 (sprite 1 wins)", dst[30])
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
	e.Set(0, Attr{X: 250, Y: 10, Pattern: 0, Palette: 0, Visible: true, Extended: true, Byte4: 0x80})

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

// TestSelectSlotSetsPatternUploadCursor pins the port $303B semantics: a write
// sets the current sprite (bits 6:0) AND the pattern-RAM upload cursor
// (bits 5:0 × 256, +128 when bit 7 set). The $5B pattern-load port then streams
// from this cursor.
func TestSelectSlotSetsPatternUploadCursor(t *testing.T) {
	e := New()

	e.SelectSlot(1) // pattern index 1 -> cursor 256
	e.WritePatternByte(0xAB)
	if got := e.PatternByte(256); got != 0xAB {
		t.Errorf("pattern[256] = %#x, want 0xAB", got)
	}

	e.SelectSlot(0x80 | 2) // pattern 2 -> 512, bit7 -> +128 = 640
	e.WritePatternByte(0xCD)
	if got := e.PatternByte(640); got != 0xCD {
		t.Errorf("pattern[640] = %#x, want 0xCD", got)
	}

	e.SelectSlot(0x45) // sprite/pattern select also sets the attribute cursor
	if e.SelectedSprite() != 0x45 {
		t.Errorf("SelectedSprite = %#x, want 0x45", e.SelectedSprite())
	}
}

// TestSprite4bppPatternAddressing pins the Next sprite pattern addressing: the
// pattern RAM is 64 slots of 256 bytes; a 4bpp sprite uses ONE 128-byte half of
// slot (byte3 bits5:0), selected by N6 (byte4 bit6). So addr = slot*256 +
// N6*128 — the same scheme port $303B/$5B upload uses.
func TestSprite4bppPatternAddressing(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	// Upload one byte to slot 2, half 1 via the real port protocol:
	// $303B = bit7(half) | slot -> cursor slot*256 + 128 = 640.
	e.SelectSlot(0x80 | 2)
	e.WritePatternByte(0x10) // high nibble 1 (opaque), low nibble 0 (transparent)

	// A 4bpp sprite (byte4 bit7=1) referencing pattern slot 2 with N6=1.
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 2, Visible: true, Extended: true, Byte4: 0xC0})
	dst := make([]byte, 64)
	for i := range dst {
		dst[i] = 0xAA // marker for "not written / transparent"
	}
	e.RenderScanline(0, dst, 64)
	if dst[0] != 1 {
		t.Errorf("4bpp px0 = %#x, want 1 (slot2 half1 @ addr 640 high nibble)", dst[0])
	}
	if dst[1] != 0xAA {
		t.Errorf("4bpp px1 = %#x, want 0xAA (low nibble 0 = transparent, untouched)", dst[1])
	}
}

// TestSpriteClipWindow pins the NextReg $19 sprite clip window: a sprite pixel
// on a scanline outside [clipY1..clipY2] (or X outside [clipX1*2..clipX2*2+1])
// is suppressed.
func TestSpriteClipWindow(t *testing.T) {
	mk := func() *Engine {
		e := New()
		e.SetEnabled(true)
		e.SelectSlot(0)
		for i := 0; i < 128; i++ {
			e.WritePatternByte(0x99) // 4bpp nibble 9, opaque
		}
		e.Set(0, Attr{X: 100, Y: 224, Pattern: 0, Visible: true})
		return e
	}
	// Clipped to Y 0..191: scanline 224 must be blank.
	e := mk()
	e.SetClip(0, 0xFF, 0, 0xBF)
	dst := make([]byte, 320)
	e.RenderScanline(224, dst, 320)
	for i := range dst {
		if dst[i] != 0 {
			t.Fatalf("clipped scanline 224 should be blank, dst[%d]=%d", i, dst[i])
		}
	}
	// No clip set: the same sprite renders.
	e2 := mk()
	dst2 := make([]byte, 320)
	e2.RenderScanline(224, dst2, 320)
	if dst2[100] == 0 {
		t.Fatal("unclipped sprite at Y=224 should render at X=100")
	}
}

// TestWriteAttr_FourByteStreamAdvances pins the port-$57 "Sprite Attribute
// Upload" behaviour (ports.txt 0x57): the current sprite (set via SelectSlot,
// i.e. port $303B) receives 4 attribute bytes, then the current-sprite pointer
// auto-advances to the next slot.
func TestWriteAttr_FourByteStreamAdvances(t *testing.T) {
	e := New()
	e.SelectSlot(5)   // port $303B selects sprite 5
	e.WriteAttr(40)   // byte 0: X LSB
	e.WriteAttr(50)   // byte 1: Y
	e.WriteAttr(0x10) // byte 2: palette offset 1, no mirror/rotate, X8=0
	e.WriteAttr(0x83) // byte 3: visible (0x80) + pattern 3, not extended
	s := e.Sprite(5)
	if s.X != 40 || s.Y != 50 || s.Palette != 1 || !s.Visible || s.Pattern != 3 || s.Extended {
		t.Fatalf("sprite 5 attrs wrong: %+v", *s)
	}
	if e.SelectedSprite() != 6 {
		t.Errorf("after 4 bytes the pointer must advance to 6, got %d", e.SelectedSprite())
	}
}

// TestWriteAttr_FiveByteWhenExtended pins that a sprite whose byte-3 bit 6 (the
// "5th byte present" flag) is set consumes 5 bytes before the pointer advances.
func TestWriteAttr_FiveByteWhenExtended(t *testing.T) {
	e := New()
	e.SelectSlot(0)
	e.WriteAttr(10)       // X
	e.WriteAttr(20)       // Y
	e.WriteAttr(0)        // attr2
	e.WriteAttr(0xC0 | 7) // byte3: visible + extended (bit6) + pattern 7
	if e.SelectedSprite() != 0 {
		t.Fatalf("must NOT advance before the 5th byte; got %d", e.SelectedSprite())
	}
	e.WriteAttr(0x40) // byte4
	if e.SelectedSprite() != 1 {
		t.Fatalf("after the 5th byte the pointer advances to 1; got %d", e.SelectedSprite())
	}
	s := e.Sprite(0)
	if !s.Extended || s.Byte4 != 0x40 {
		t.Errorf("byte4 not applied: %+v", *s)
	}
}

// TestWriteAttr_WrapsAt127 pins that the current-sprite pointer wraps 127->0.
func TestWriteAttr_WrapsAt127(t *testing.T) {
	e := New()
	e.SelectSlot(127)
	for i := 0; i < 4; i++ {
		e.WriteAttr(byte(i)) // byte3 = 3: not visible, not extended
	}
	if e.SelectedSprite() != 0 {
		t.Errorf("must wrap 127->0, got %d", e.SelectedSprite())
	}
}

// TestNonExtendedSpriteIs8bpp pins that a sprite WITHOUT a 5th attribute byte
// (byte 3 bit 6 clear) renders as 8bpp — one palette index per pattern byte —
// per the FPGA (sprites.vhd:931: the 4bpp "H" bit requires attr_4(7) AND the
// 5th byte present).
func TestNonExtendedSpriteIs8bpp(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	// Pattern slot 0: an 8bpp row where byte 0 = index 0x2A, rest 0 (transparent).
	e.SetPatternAddr(0)
	e.WritePatternByte(0x2A)
	// A non-extended sprite at (0,0), pattern 0.
	e.Set(0, Attr{X: 0, Y: 0, Pattern: 0, Visible: true})
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if dst[0] != 0x2A {
		t.Errorf("non-extended sprite must be 8bpp: dst[0]=$%02X, want $2A (the raw pattern byte)", dst[0])
	}
}
