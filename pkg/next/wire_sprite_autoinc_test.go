package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// TestSpriteAttrAutoIncrement: NR$75-$79 apply the attribute byte to the
// selected sprite AND auto-increment the sprite index; NR$35-$39 don't.
func TestSpriteAttrAutoIncrement(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	WireSprites(disp, e)

	disp.Select(0x34)
	disp.WriteData(5) // select sprite 5
	if e.SelectedSprite() != 5 {
		t.Fatalf("select: got %d, want 5", e.SelectedSprite())
	}
	// $75 = byte 0 (X LSB) with auto-increment: applies to sprite 5, bumps.
	disp.Select(0x75)
	disp.WriteData(0x42)
	if got := e.Sprite(5).X; got != 0x42 {
		t.Errorf("sprite 5 X = %#x, want 0x42", got)
	}
	if e.SelectedSprite() != 6 {
		t.Errorf("after $75: select = %d, want 6 (auto-incremented)", e.SelectedSprite())
	}
	// $35 = byte 0 without auto-increment: applies to sprite 6, no bump.
	disp.Select(0x35)
	disp.WriteData(0x11)
	if got := e.Sprite(6).X; got != 0x11 {
		t.Errorf("sprite 6 X = %#x, want 0x11", got)
	}
	if e.SelectedSprite() != 6 {
		t.Errorf("after $35: select = %d, want 6 (unchanged)", e.SelectedSprite())
	}
}
