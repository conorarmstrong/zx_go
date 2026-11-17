package palette

import "testing"

// The FPGA's palette RAM has no initial contents (ram/init/none.bin.txt); the
// boot firmware loads the defaults. LoadFirmwareDefaults is that state, for
// machines built without running the firmware.
func TestLoadFirmwareDefaults(t *testing.T) {
	b := NewBank()
	b.LoadFirmwareDefaults()
	ula := b.Palette(int(PaletteULAFirst))
	for _, c := range []struct {
		idx  byte
		want uint16
	}{
		{0, 0x000},    // black ink
		{3, 0x145},    // magenta ink: 101 000 101
		{11, 0x1CF},   // bright magenta ink: 111 001 111, kept off $E3
		{16, 0x000},   // black paper
		{27, 0x1CF},   // bright magenta paper
		{0xE3, 0x145}, // the pattern repeats every 16
		{15, 0x1FF},   // bright white ink
	} {
		if got := ula.Get(c.idx); got != c.want {
			t.Errorf("ULA entry %d = %#03x, want %#03x", c.idx, got, c.want)
		}
	}
	if got := b.Palette(int(PaletteTilemapFirst)).Get(5); got != 0 {
		t.Errorf("tilemap entry 5 = %#03x, want 0 (the firmware leaves it)", got)
	}
	l2 := b.Palette(int(PaletteLayer2First))
	if got := l2.Get(0xE3); got != 0xE3<<1|1 {
		t.Errorf("Layer 2 entry $E3 = %#03x, want the identity %#03x", got, 0xE3<<1|1)
	}
	if got := l2.Get(0x10); got != 0x10<<1 {
		t.Errorf("Layer 2 entry $10 = %#03x, want %#03x", got, 0x10<<1)
	}
}
