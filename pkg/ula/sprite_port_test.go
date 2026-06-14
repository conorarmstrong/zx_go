package ula

import "testing"

type fakeSpritePort struct {
	selected byte
	status   byte
	reads    int
}

func (f *fakeSpritePort) SelectSprite(v byte) { f.selected = v }
func (f *fakeSpritePort) ReadStatus() byte    { f.reads++; return f.status }

// TestNextSpritePortRouting verifies port $303B writes select a sprite and
// reads return the sprite status through the NextSpritePort hook.
func TestNextSpritePortRouting(t *testing.T) {
	u := &ULA{}
	fp := &fakeSpritePort{status: 0x01}
	u.SetNextSpritePort(fp)

	u.writePortInternal(0x303B, 0x05)
	if fp.selected != 0x05 {
		t.Errorf("$303B write: selected = %#x, want 0x05", fp.selected)
	}

	v, ok := u.readPortInternal(0x303B)
	if !ok || v != 0x01 {
		t.Errorf("$303B read: got %#x ok=%v, want 0x01 true", v, ok)
	}
	if fp.reads != 1 {
		t.Errorf("ReadStatus called %d times, want 1", fp.reads)
	}
}
