package sprite

import "testing"

// TestSpriteMirrorRotate covers the per-sprite attribute-byte-2 transform
// (FPGA sprites.vhd:813): bit 3 = X mirror, bit 2 = Y mirror, bit 1 =
// rotate (swaps X/Y and inverts the X mirror).
func TestSpriteMirrorRotate(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	s := e.Sprite(0)
	s.Visible = true
	s.X = 0
	s.Y = 0
	s.Pattern = 0
	s.Extended, s.Byte4 = true, 0x80 // 4bpp (byte4 bit7), scale 1×
	e.pattern[0] = 0x90              // pattern 0 row 0: pixel (0,0) = nibble 9

	at := func(xm, ym, rot bool, x, y int) byte {
		s.XMirror, s.YMirror, s.Rotate = xm, ym, rot
		dst := make([]byte, 320)
		e.RenderScanline(y, dst, 320)
		return dst[x]
	}
	if got := at(false, false, false, 0, 0); got != 9 {
		t.Errorf("identity: pixel (0,0) = %d, want 9", got)
	}
	// X mirror: the (0,0) pixel renders at display x=15.
	if at(true, false, false, 15, 0) != 9 || at(true, false, false, 0, 0) != 0 {
		t.Errorf("X mirror: want pixel at display x=15, got %d; x=0 got %d",
			at(true, false, false, 15, 0), at(true, false, false, 0, 0))
	}
	// Y mirror: the (0,0) pixel renders at display row 15.
	if at(false, true, false, 0, 15) != 9 || at(false, true, false, 0, 0) != 0 {
		t.Errorf("Y mirror: want pixel at row 15, got %d; row 0 got %d",
			at(false, true, false, 0, 15), at(false, true, false, 0, 0))
	}
}

// TestSpriteScaleAndN6 covers the byte-4 scale (bits 4:3 X, 2:1 Y) and
// the N6 pattern-high bit (byte-4 bit 6), per FPGA sprites.vhd:437.
func TestSpriteScaleAndN6(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	s := e.Sprite(0)
	s.Visible = true
	s.X = 0
	s.Y = 0

	// N6: byte-4 bit 6 selects the upper 128-byte HALF of the 256-byte pattern
	// slot; bit 7 = 4bpp; scale 1× → 0xC0. Slot 0 + N6 → addr 0*256+128 = 128.
	s.Pattern = 0
	s.Extended = true
	s.Byte4 = 0xC0
	e.pattern[128] = 0x90 // slot 0 upper half, pixel (0,0) = 9
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if dst[0] != 9 {
		t.Errorf("N6: slot-0 upper-half pixel = %d, want 9", dst[0])
	}

	// 2× scale (bit 7 = 4bpp, bits 4:3 = 01 X, 2:1 = 01 Y → 0x8A):
	// pattern 0's (0,0) pixel covers display (0..1, 0..1).
	s.Pattern = 0
	s.Byte4 = 0x8A
	e.pattern[0] = 0x90
	for y := 0; y < 2; y++ {
		d := make([]byte, 320)
		e.RenderScanline(y, d, 320)
		if d[0] != 9 || d[1] != 9 {
			t.Errorf("2× scale y=%d: d[0]=%d d[1]=%d, want 9,9", y, d[0], d[1])
		}
	}
}

// TestSpriteAnchorRelative covers anchor/relative sprites: a relative
// sprite (byte-4 bits 7:6 == "01") is positioned at anchor + offset, and
// inherits the anchor's visibility.
func TestSpriteAnchorRelative(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	e.pattern[0] = 0x90 // pattern 0 pixel (0,0) = 9

	// Anchor: sprite 0 at (100,50), 4bpp (byte-4 bit7 = H), composite.
	a0 := e.Sprite(0)
	a0.Visible, a0.X, a0.Y, a0.Pattern = true, 100, 50, 0
	a0.Extended, a0.Byte4 = true, 0x80
	// Relative: sprite 1, offset (+10,+5), byte-4 bits 7:6 == "01".
	a1 := e.Sprite(1)
	a1.Visible, a1.Pattern = true, 0
	a1.X, a1.Y = 10, 5 // relative offsets
	a1.Extended, a1.Byte4 = true, 0x40

	dst := make([]byte, 320)
	e.RenderScanline(50, dst, 320)
	if dst[100] != 9 {
		t.Errorf("anchor: dst[100] @ y=50 = %d, want 9", dst[100])
	}
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(55, dst, 320)
	if dst[110] != 9 {
		t.Errorf("relative: dst[110] @ y=55 = %d, want 9 (anchor 100+10, 50+5)", dst[110])
	}
	// Anchor invisible → relative invisible.
	a0.Visible = false
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(55, dst, 320)
	if dst[110] != 0 {
		t.Errorf("relative should be invisible when anchor is: dst[110] = %d", dst[110])
	}
}

// TestSpriteCollision: the collision flag latches when two opaque sprite
// pixels overlap, and clears when the status is read.
func TestSpriteCollision(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	e.pattern[0] = 0x90 // pattern 0 pixel (0,0) = 9 (opaque), rest transparent

	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x01 != 0 {
		t.Error("no sprites: collision should be clear")
	}

	// Two visible sprites both painting display pixel (0,0).
	s0 := e.Sprite(0)
	s0.Visible, s0.X, s0.Y, s0.Pattern = true, 0, 0, 0
	s1 := e.Sprite(1)
	s1.Visible, s1.X, s1.Y, s1.Pattern = true, 0, 0, 0
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x01 == 0 {
		t.Error("overlapping sprites: collision should latch")
	}
	if e.ReadStatus()&0x01 != 0 {
		t.Error("collision should clear on read")
	}

	// Move sprite 1 away — no overlap, no collision.
	s1.X = 100
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x01 != 0 {
		t.Error("non-overlapping sprites: no collision")
	}
}

// TestSprite4bppPaletteOffset: in 4bpp the palette offset is the index's
// HIGH nibble (offset<<4 | pixel), per FPGA sprites.vhd:968.
func TestSprite4bppPaletteOffset(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	s := e.Sprite(0)
	s.Visible = true
	s.Palette = 3
	s.Extended, s.Byte4 = true, 0x80 // 4bpp (byte4 bit7), scale 1×
	e.pattern[0] = 0x50              // pixel (0,0) = nibble 5
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if dst[0] != 0x35 { // (3<<4)|5
		t.Errorf("4bpp palette offset: %#x, want 0x35", dst[0])
	}
}

// TestSprite8bpp covers 8bpp mode (byte-4 bit 7 clear): 256 bytes/pattern,
// 1 byte/pixel, and the palette offset added to the byte's high nibble
// (FPGA sprites.vhd:968).
func TestSprite8bpp(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	s := e.Sprite(0)
	s.Visible = true
	s.Extended = true
	s.Byte4 = 0x00      // bit 7 clear → 8bpp, scale 1×
	e.pattern[0] = 0x42 // 8bpp pattern 0, pixel (0,0) = 0x42
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if dst[0] != 0x42 {
		t.Errorf("8bpp pixel = %#x, want 0x42", dst[0])
	}
	s.Palette = 3
	dst2 := make([]byte, 320)
	e.RenderScanline(0, dst2, 320)
	if dst2[0] != 0x72 { // 0x42 + (3<<4)
		t.Errorf("8bpp + palette offset: %#x, want 0x72", dst2[0])
	}
}
