package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// TestWireNR15EnablesSprites pins that NR$15 bit 0 ("Enable sprites", per
// registers.txt 0x15) drives the sprite engine's master enable. Sonic relies
// on this: it sets up its sprites (sonic + the HUD) and turns them on with
// NR$15=$45 (bit 0 set). Without the wiring the sprite engine stayed disabled
// and sonic/HUD never rendered, even once the level tilemap was visible.
func TestWireNR15EnablesSprites(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	p := &LayerPriority{}
	WireLayerPriority(disp, p, e)

	if e.Enabled() {
		t.Fatal("sprite engine should start disabled")
	}

	disp.Select(0x15)
	disp.WriteData(0x45) // bit 0 = 1 -> enable sprites (+ priority bits)
	if !e.Enabled() {
		t.Error("NR$15 bit 0 = 1 must enable the sprite engine")
	}
	if p.Get() != 0x45 {
		t.Errorf("NR$15 value must still be stored for priority: got %#x", p.Get())
	}

	disp.WriteData(0x44) // bit 0 = 0 -> disable sprites
	if e.Enabled() {
		t.Error("NR$15 bit 0 = 0 must disable the sprite engine")
	}
}

// TestWireNR15SpritePriority pins that NR$15 bit 6 ("sprite_priority" /
// zero-on-top, sprites.vhd:73) drives the sprite engine's priority mode. Sonic
// sets it (NR$15=$45) so the lowest-numbered overlapping sprite is drawn in
// front — the HUD relies on this layering.
func TestWireNR15SpritePriority(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	p := &LayerPriority{}
	WireLayerPriority(disp, p, e)

	if e.ZeroOnTop() {
		t.Fatal("zero-on-top should start off (reset default)")
	}
	disp.Select(0x15)
	disp.WriteData(0x45) // bit 6 = 1 -> zero-on-top enabled
	if !e.ZeroOnTop() {
		t.Error("NR$15 bit 6 = 1 must enable zero-on-top sprite priority")
	}
	disp.WriteData(0x05) // bit 6 = 0 -> zero-on-top disabled (bit 0 still set)
	if e.ZeroOnTop() {
		t.Error("NR$15 bit 6 = 0 must disable zero-on-top sprite priority")
	}
}

// TestWireNR15OverBorder pins NR$15 bit 1 ("sprites over border") and bit 5
// ("border clip enable") → the sprite engine clip mode (sprites.vhd:1043-1066).
// With over-border off (Sonic's NR$15=$45) sprites in the top-border band are
// clipped to the paper area; a sprite parked at Y=1 (Sonic's stray HUD row,
// the garbled top-right icons) is hidden.
func TestWireNR15OverBorder(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	e.SetEnabled(true)
	e.SetClip(0, 255, 0, 191)
	for i := 0; i < 128; i++ {
		e.SetPatternAddr(uint16(i))
		e.WritePatternByte(0x11)
	}
	p := &LayerPriority{}
	WireLayerPriority(disp, p, e)

	disp.Select(0x15)
	disp.WriteData(0x45) // Sonic: bit1=0 (over-border off), bit5=0
	e.Set(0, sprite.Attr{X: 40, Y: 1, Pattern: 0, Visible: true, Extended: true, Byte4: 0x80})
	dst := make([]byte, 320)
	e.RenderScanline(8, dst, 320)
	if dst[40] != 0 {
		t.Errorf("NR$15=$45 (over-border off): Y=1 sprite drew $%02X at scanline 8, want 0 (clipped)", dst[40])
	}

	disp.WriteData(0x47) // bit1=1 (over-border on), bit5=0 -> no clip
	for i := range dst {
		dst[i] = 0
	}
	e.RenderScanline(8, dst, 320)
	if dst[40] != 1 {
		t.Errorf("NR$15=$47 (over-border on): Y=1 sprite should draw, got $%02X", dst[40])
	}
}
