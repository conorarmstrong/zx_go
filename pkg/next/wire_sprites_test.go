package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

func TestWireSpritesAttributePath(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	WireSprites(disp, e)

	// Select sprite 7
	disp.Select(0x34)
	disp.WriteData(7)
	if e.SelectedSprite() != 7 {
		t.Errorf("after 0x34=7: SelectedSprite=%d", e.SelectedSprite())
	}

	// X low = 0x42
	disp.Select(0x35)
	disp.WriteData(0x42)
	// Y = 100
	disp.Select(0x36)
	disp.WriteData(100)
	// Palette offset 0x20 (high nibble) + X9=1 (bit 0)
	disp.Select(0x37)
	disp.WriteData(0x21)
	// Visible + pattern 0x05
	disp.Select(0x38)
	disp.WriteData(0x85)

	s := e.Sprite(7)
	if s == nil {
		t.Fatal("Sprite(7) nil")
	}
	if s.X != 0x142 {
		t.Errorf("X = %#x, want 0x142 (low 0x42 + X9)", s.X)
	}
	if s.Y != 100 {
		t.Errorf("Y = %d, want 100", s.Y)
	}
	if s.Palette != 0x02 {
		t.Errorf("Palette = %#x, want 0x02 (high nibble of 0x21)", s.Palette)
	}
	if !s.Visible {
		t.Errorf("Visible should be true")
	}
	if s.Pattern != 0x05 {
		t.Errorf("Pattern = %#x, want 0x05", s.Pattern)
	}
}

func TestWireSpritesIgnoresOutOfRangeSelection(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	WireSprites(disp, e)

	// SelectSprite masks to 0x7F, so 0xFF -> 0x7F (still in range).
	disp.Select(0x34)
	disp.WriteData(0xFF)
	if e.SelectedSprite() != 0x7F {
		t.Errorf("after 0x34=0xFF: SelectedSprite=%d, want 0x7F", e.SelectedSprite())
	}
}
