package tilemap

import "testing"

// newNibbleFixture builds a one-tile map whose tile 1 is entirely
// nibble 0, with the given per-tile attribute byte.
func newNibbleFixture(attr byte) *Tilemap {
	buf := make([]byte, 0x4000)
	// Map entry 0: tile id 1, attribute byte.
	buf[0] = 0x01
	buf[1] = attr
	// Tile 1's 32 bytes (at tiles base + 1*32) stay zero: every pixel
	// is nibble 0.
	tm := New(&fakeBank{data: buf})
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetEnabled(true)
	tm.SetMode40()
	return tm
}

// The FPGA assembles a tilemap pixel as the attribute's palette offset
// in the high nibble and the tile's pixel nibble in the low one,
// unconditionally (video/tilemap.vhd:382-383):
//
//	tm_tilemap_pixel_data_standard(7 downto 4) <= tm_tilemap_1(7 downto 4);
//	tm_tilemap_pixel_data_standard(3 downto 0) <= tm_mem_data_i(...);
//
// Transparency is decided downstream by comparing the LOW nibble
// against NR$4C (reset $F), so nibble 0 is an ordinary opaque index
// $00, $10, $20 … We special-cased nibble 0 to a bare 0 and threw the
// palette offset away, so a background tile drawn with offset 3 came
// out as palette[0] instead of palette[$30].
func TestNibbleZeroKeepsThePaletteOffset(t *testing.T) {
	tm := newNibbleFixture(0x30) // palette offset 3
	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)

	if got := dst[0]; got != 0x30 {
		t.Errorf("nibble-0 pixel = %#02x, want %#02x (offset 3 << 4)", got, 0x30)
	}
}

// Offset 0 is the common case and must still produce index 0.
func TestNibbleZeroWithNoOffsetStaysIndexZero(t *testing.T) {
	tm := newNibbleFixture(0x00)
	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)

	if got := dst[0]; got != 0x00 {
		t.Errorf("nibble-0 pixel with offset 0 = %#02x, want 0x00", got)
	}
}

// The per-pixel "below ULA" plane is (attr bit 0 OR mode_512) AND NOT
// on_top (video/tilemap.vhd:388). The compositor used to approximate it
// by treating any nibble-0 pixel as below, which meant a background
// tile with a palette offset never appeared at all.
func TestBelowPlaneFollowsTheAttributeBit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attr  byte
		onTop bool
		want  byte
	}{
		{"attr bit 0 clear", 0x30, false, 0},
		{"attr bit 0 set", 0x31, false, 1},
		{"attr bit 0 set but on_top", 0x31, true, 0},
	} {
		tm := newNibbleFixture(tc.attr)
		if tc.onTop {
			tm.SetControl(0x01)
		}
		dst := make([]byte, 256)
		below := make([]byte, 256)
		tm.RenderScanlineBelow(0, dst, below)
		if below[0] != tc.want {
			t.Errorf("%s: below = %d, want %d", tc.name, below[0], tc.want)
		}
	}
}

// 512-tile mode repurposes attribute bit 0 as the tile index's 9th bit,
// so the FPGA forces the below flag on for every pixel.
func TestBelowPlaneIsForcedIn512TileMode(t *testing.T) {
	tm := newNibbleFixture(0x30) // attr bit 0 clear
	tm.SetControl(0x02)
	dst := make([]byte, 256)
	below := make([]byte, 256)
	tm.RenderScanlineBelow(0, dst, below)
	if below[0] != 1 {
		t.Errorf("512-tile mode: below = %d, want 1", below[0])
	}
}
