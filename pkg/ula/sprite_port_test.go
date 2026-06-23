package ula

import "testing"

type fakeSpritePort struct {
	selected byte
	slot     byte
	pattern  []byte
	attrs    []byte
	status   byte
	reads    int
}

func (f *fakeSpritePort) SelectSprite(v byte)     { f.selected = v }
func (f *fakeSpritePort) SelectSlot(v byte)       { f.slot = v }
func (f *fakeSpritePort) WritePatternByte(v byte) { f.pattern = append(f.pattern, v) }
func (f *fakeSpritePort) WriteAttr(v byte)        { f.attrs = append(f.attrs, v) }
func (f *fakeSpritePort) ReadStatus() byte        { f.reads++; return f.status }

// TestNextSpritePortRouting verifies port $303B writes select a sprite and
// reads return the sprite status through the NextSpritePort hook.
func TestNextSpritePortRouting(t *testing.T) {
	u := &ULA{}
	fp := &fakeSpritePort{status: 0x01}
	u.SetNextSpritePort(fp)

	u.writePortInternal(0x303B, 0x05)
	if fp.slot != 0x05 {
		t.Errorf("$303B write: slot = %#x, want 0x05", fp.slot)
	}

	v, ok := u.readPortInternal(0x303B)
	if !ok || v != 0x01 {
		t.Errorf("$303B read: got %#x ok=%v, want 0x01 true", v, ok)
	}
	if fp.reads != 1 {
		t.Errorf("ReadStatus called %d times, want 1", fp.reads)
	}
}

// TestNextSpritePatternUpload verifies port $005B streams bytes to the sprite
// pattern RAM, decoded on the low byte only (so an OTIR loop whose B register
// varies the high byte still routes every byte). Without this routing sonic's
// sprites (and HUD) had no pattern data and never rendered.
func TestNextSpritePatternUpload(t *testing.T) {
	u := &ULA{}
	fp := &fakeSpritePort{}
	u.SetNextSpritePort(fp)

	// OTIR drives the port as $FF5B, $FE5B, ... (B counts down); all must route.
	u.writePortInternal(0x005B, 0x11)
	u.writePortInternal(0xFF5B, 0x22)
	u.writePortInternal(0x025B, 0x33)
	if len(fp.pattern) != 3 || fp.pattern[0] != 0x11 || fp.pattern[1] != 0x22 || fp.pattern[2] != 0x33 {
		t.Errorf("pattern upload via $5B = %#v, want [0x11 0x22 0x33]", fp.pattern)
	}
}

// TestNextSpriteAttrUpload verifies port $0057 ("Sprite Attribute Upload")
// streams attribute bytes to the sprite engine, decoded on the low byte only
// (the OTIR upload loop varies the high byte via B). Nextoid uploads every
// sprite (bat, ball, HUD) through this port each frame; without the routing
// the sprite engine never sees them and they stay invisible.
func TestNextSpriteAttrUpload(t *testing.T) {
	u := &ULA{}
	fp := &fakeSpritePort{}
	u.SetNextSpritePort(fp)

	u.writePortInternal(0x0057, 0xAA)
	u.writePortInternal(0xFF57, 0xBB) // B-varied high byte must still route
	u.writePortInternal(0x0257, 0xCC)
	if len(fp.attrs) != 3 || fp.attrs[0] != 0xAA || fp.attrs[1] != 0xBB || fp.attrs[2] != 0xCC {
		t.Errorf("attribute upload via $57 = %#v, want [0xAA 0xBB 0xCC]", fp.attrs)
	}
}
