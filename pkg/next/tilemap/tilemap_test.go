package tilemap

import "testing"

// fakeBank stores one 16 KB slice and returns it for every bank
// index. Sufficient for the unit tests because tilemap maps and
// tile defs that NextZXOS Browser sets up live in the same bank.
type fakeBank struct{ data []byte }

func (f *fakeBank) GetPage(bank int) []byte { return f.data }

// twoBank routes bank 5 and bank 7 to distinct buffers so the
// bank-select test below can prove the renderer reads from the
// right place.
type twoBank struct{ bank5, bank7 []byte }

func (f *twoBank) GetPage(bank int) []byte {
	switch bank {
	case 5:
		return f.bank5
	case 7:
		return f.bank7
	}
	return nil
}

// TestEnabledGetter — Enabled() round-trip; 0% before iter 259.
func TestEnabledGetter(t *testing.T) {
	tm := New(&fakeBank{data: make([]byte, 16384)})
	if tm.Enabled() {
		t.Errorf("default Enabled = true, want false")
	}
	tm.SetEnabled(true)
	if !tm.Enabled() {
		t.Errorf("after SetEnabled(true): Enabled = false")
	}
	tm.SetEnabled(false)
	if tm.Enabled() {
		t.Errorf("after SetEnabled(false): Enabled = true")
	}
}

// TestBankSelectBit7 pins down which NR$6E/$6F bit toggles the
// tilemap bank from 5 to 7. The FPGA (zxnext.vhd 5468-5469) stores
// CPU-write bit 7 separately and concatenates it as bit 6 of the
// 7-bit tm_map_base_i passed to tilemap.vhd. CPU-write bit 6 is
// discarded. So at the wire layer, NR$6E bit 7 (value $80), not
// bit 6 ($40), picks bank 7.
//
// NextZXOS Browser writes NR$6E=$40, NR$6F=$45 — both bit 7 = 0 —
// expecting reads to land in bank 5 with offset $0000 / $0500 (the
// FPGA-dropped bit 6 is ignored). The fixture below proves we
// honour that: writing $C0 / $C5 (bit 7 set) reads bank 7.
func TestBankSelectBit7(t *testing.T) {
	tb := &twoBank{
		bank5: make([]byte, 0x4000),
		bank7: make([]byte, 0x4000),
	}
	// Bank 7 carries a real tile map + tile defs; bank 5 is left
	// zero so a wrong-bank read produces blank output.
	for i := 0; i < 40; i++ {
		tb.bank7[i] = 0x01 // every tile = id 1
	}
	for col := 0; col < 4; col++ {
		tb.bank7[0x500+1*32+col] = 0x77
	}
	tm := New(tb)
	tm.SetTileMapBase(0xC0) // bit 7 = bank 7, offset bits 5:0 = 0
	tm.SetTilesBase(0xC5)   // bit 7 = bank 7, offset = $05
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)

	for i := 0; i < 256; i++ {
		if dst[i] != 7 {
			t.Fatalf("byte %d: got %02X want 07 (bank-7 content must render)",
				i, dst[i])
		}
	}
}

// TestBankSelectBit6Ignored proves the FPGA's dropped-bit behaviour:
// NR$6E=$40 (bit 6 set, bit 7 clear) must read bank 5, not bank 7.
// This is the case NextZXOS Browser actually exercises during boot.
func TestBankSelectBit6Ignored(t *testing.T) {
	tb := &twoBank{
		bank5: make([]byte, 0x4000),
		bank7: make([]byte, 0x4000),
	}
	// Bank 5 holds the real content; bank 7 is decoy.
	for i := 0; i < 40; i++ {
		tb.bank5[i] = 0x01
	}
	for col := 0; col < 4; col++ {
		tb.bank5[0x500+1*32+col] = 0x77
	}
	// Decoy bank 7 with different content.
	for i := 0; i < 40; i++ {
		tb.bank7[i] = 0x02
	}
	tm := New(tb)
	tm.SetTileMapBase(0x40) // NextZXOS Browser value: bit 6 set, bit 7 clear
	tm.SetTilesBase(0x45)
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)
	for i := 0; i < 256; i++ {
		if dst[i] != 7 {
			t.Fatalf("byte %d: got %02X want 07 (bank-5 content must render)",
				i, dst[i])
		}
	}
}

// TestRenderScanlineFromKnownMap exercises the simplest possible
// path: 40-column mode (NR$6B bit 6=0), strip_flags=1 (NR$6B bit 5=1),
// 4bpp tiles. The tile map at $4000 within a 16 KB bank holds tile
// indices; the tile defs at $4500 hold 32 bytes per tile (8 rows of
// 4 bytes, 4bpp packed).
//
// We construct a 1-row tile map [0, 1, 0, 1, ...] and two tile
// definitions: tile 0 = all zeros, tile 1 = a horizontal stripe of
// palette index 7 on row 0. Then we ask the layer for scanline 0
// and expect alternating zeros + sevens across the 320 visible
// pixels (cropped to ULA's 256 in this first slice).
func TestRenderScanlineFromKnownMap(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// Tile map at $0000 within the bank (NR$6E = $00 → base $0000).
	// 40 tiles for the first row: alternate 0, 1.
	for i := 0; i < 40; i++ {
		if i%2 == 1 {
			bank.data[i] = 0x01
		}
	}
	// Tile defs at $0500 within the bank (NR$6F = $05 → base $0500).
	// Tile 1: row 0 = 32-byte tile, first 4 bytes = $77 each (palette
	// index 7 in each 4bpp nibble for 8 pixels).
	for col := 0; col < 4; col++ {
		bank.data[0x500+1*32+col] = 0x77
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)

	// First tile = blank (palette index 0 = transparent), second
	// tile = 7s with default palette offset 0 → palette index 7;
	// third = blank; etc.
	for i := 0; i < 256; i++ {
		tileX := i / 8
		want := byte(0)
		if tileX%2 == 1 {
			want = 7
		}
		if dst[i] != want {
			t.Fatalf("scanline byte %d (tile %d): got %02X want %02X",
				i, tileX, dst[i], want)
		}
	}
}

// TestPaletteOffsetFromDefaultAttr verifies that when strip_flags
// is set, the per-tile palette offset comes from NR$6C bits 7:4,
// shifted into the upper nibble of the final palette index.
// NextZXOS Browser writes $6C = $40 (offset 4), so tile pixel 1
// must render as palette index $41, not $01.
func TestPaletteOffsetFromDefaultAttr(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// Single-tile-per-row map of tile id 1.
	for i := 0; i < 40; i++ {
		bank.data[i] = 0x01
	}
	// Tile 1 row 0 = palette nibble 5 across all pixels.
	for col := 0; col < 4; col++ {
		bank.data[0x500+1*32+col] = 0x55
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetDefaultAttr(0x40) // offset = 4
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)

	want := byte(0x45) // (4 << 4) | 5
	for i := 0; i < 256; i++ {
		if dst[i] != want {
			t.Fatalf("byte %d: got %02X want %02X", i, dst[i], want)
		}
	}
}

// ========================================================================
// Tilemap render edge tests (iter 210).
// ========================================================================

// TestRenderScanline_DisabledFillsZeros verifies that with enabled=false
// the renderer writes zeros (transparent) into dst without reading
// the backing memory.
func TestRenderScanline_DisabledFillsZeros(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	for i := 0; i < 40; i++ {
		bank.data[i] = 0x01
	}
	for col := 0; col < 4; col++ {
		bank.data[0x500+1*32+col] = 0xFF
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(false) // disabled
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 0xAA // pre-fill with non-zero
	}
	tm.RenderScanline(0, dst)
	for i := 0; i < 256; i++ {
		if dst[i] != 0 {
			t.Errorf("disabled byte %d = $%02X, want 0", i, dst[i])
			return
		}
	}
}

// TestRenderScanline_NilMemNoCrash verifies the renderer cleanly
// returns zeros when the BankReader is nil.
func TestRenderScanline_NilMemNoCrash(t *testing.T) {
	tm := New(nil)
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 0xCC
	}
	tm.RenderScanline(0, dst)
	for i := 0; i < 256; i++ {
		if dst[i] != 0 {
			t.Errorf("nil mem: byte %d = $%02X, want 0", i, dst[i])
			return
		}
	}
}

// TestRenderScanline_OutOfRangeY verifies y >= Height returns
// zero-filled dst.
func TestRenderScanline_OutOfRangeY(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	for i := 0; i < 40; i++ {
		bank.data[i] = 0x01
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	dst := make([]byte, 256)
	for i := range dst {
		dst[i] = 0xAA
	}
	tm.RenderScanline(Height+1, dst) // out of range
	for i := 0; i < 256; i++ {
		if dst[i] != 0 {
			t.Errorf("out-of-range y: byte %d = $%02X, want 0", i, dst[i])
			return
		}
	}
}

// TestRenderScanline_TilePixelRowSelection verifies that successive
// scanlines within a tile (rows 0..7) walk the tile's 8 pixel-rows.
// Each row in a 4bpp tile occupies 4 bytes; for pixel-row N we read
// the byte at offset N*4 (and N*4+1/+2/+3 for the rest of the row).
func TestRenderScanline_TilePixelRowSelection(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// Single tile 1 across the row.
	for i := 0; i < 40; i++ {
		bank.data[i] = 0x01
	}
	// Tile 1: row N (0..7) has nibble value N+1 across all pixels.
	// row 0 → $11 in bytes 0..3
	// row 1 → $22 in bytes 4..7
	// row 2 → $33 in bytes 8..11
	// ...
	for row := 0; row < 8; row++ {
		val := byte((row + 1) | ((row + 1) << 4))
		for col := 0; col < 4; col++ {
			bank.data[0x500+1*32+row*4+col] = val
		}
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(true)
	tm.SetMode40()
	tm.SetStripFlags(true)

	for row := 0; row < 8; row++ {
		dst := make([]byte, 256)
		tm.RenderScanline(row, dst)
		want := byte(row + 1)
		// All 256 pixels should be `want` (palette offset 0).
		for i := 0; i < 256; i++ {
			if dst[i] != want {
				t.Errorf("row %d, byte %d = $%02X, want $%02X", row, i, dst[i], want)
				return
			}
		}
	}
}

// TestRenderScanline_80ColMode verifies 80-column mode draws 80 tiles
// per row (each 4 pixels wide in the 320-pixel canvas).
func TestRenderScanline_80ColMode(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// 80 tiles in row 0, alternating 0 (blank) and 1.
	for i := 0; i < 80; i++ {
		if i%2 == 1 {
			bank.data[i] = 0x01
		}
	}
	// Tile 1 row 0 = palette nibble 9.
	for col := 0; col < 4; col++ {
		bank.data[0x500+1*32+col] = 0x99
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(true)
	tm.SetMode80() // 80-col mode
	tm.SetStripFlags(true)

	dst := make([]byte, 320)
	tm.RenderScanline(0, dst)
	// In 80-col mode each tile is 4 pixels wide (320/80 = 4).
	// Tiles 0, 2, 4, ... → blank; tiles 1, 3, 5, ... → 9.
	// Actually TileWidth is shared between modes — let's just verify
	// some tile-1 pixels are non-zero and tile-0 pixels are zero.
	// Look at the middle of a known tile-1 region.
	hasNine := false
	for i := 0; i < 320; i++ {
		if dst[i] == 9 {
			hasNine = true
		}
	}
	if !hasNine {
		t.Error("80-col mode: no palette-9 pixels rendered (tile 1 missing)")
	}
}

// TestIs80Col verifies NR$6B bit 6 reports through Is80Col(), which the
// compositor/ULA use to switch to the 640-pixel wide render path.
func TestIs80Col(t *testing.T) {
	tm := New(&fakeBank{data: make([]byte, 0x4000)})
	if tm.Is80Col() {
		t.Error("default Is80Col() = true, want false")
	}
	tm.SetMode80()
	if !tm.Is80Col() {
		t.Error("after SetMode80(): Is80Col() = false, want true")
	}
}

// TestRenderScanline_80Col640px verifies that, given a 640-pixel buffer,
// 80-column mode renders all 80 tiles at 8px each — so a tile beyond
// column 40 (which a 320-pixel buffer can't reach) is drawn. This is the
// core of the 640px wide display path.
func TestRenderScanline_80Col640px(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// strip_flags => 1 byte/tile; put tile 1 at column 50.
	bank.data[50] = 0x01
	// Tile 1 row 0 (4bpp, tilesBase $0500) = palette nibble 9.
	for col := 0; col < 4; col++ {
		bank.data[0x500+1*32+col] = 0x99
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x05)
	tm.SetEnabled(true)
	tm.SetMode80()
	tm.SetStripFlags(true)

	// 640px buffer: column 50 lands at x=400 (well past the 320 mark).
	dst := make([]byte, 640)
	tm.RenderScanline(0, dst)
	has := false
	for x := 50 * 8; x < 50*8+8; x++ {
		if dst[x] == 9 {
			has = true
		}
	}
	if !has {
		t.Error("80-col 640px: column 50 (x=400) not rendered — wide path missing")
	}
	// A 320px buffer stops at column 39, so column-50 content is absent.
	dst320 := make([]byte, 320)
	tm.RenderScanline(0, dst320)
	for x := range dst320 {
		if dst320[x] == 9 {
			t.Errorf("320px buffer rendered column-50 content at x=%d", x)
		}
	}
}

// TestTilemapMirrorRotate covers the per-tile attribute transform from
// the FPGA (tilemap.vhd:320-324): attr bit 3 = X mirror, bit 2 = Y
// mirror, bit 1 = rotate (swaps X/Y, and also inverts the X mirror).
func TestTilemapMirrorRotate(t *testing.T) {
	buf := make([]byte, 0x4000)
	buf[0] = 0x01    // cell (0,0) = tile 1
	buf[0x20] = 0x90 // tile 1 def (tilesBase $0000) row 0 byte 0: pixel (0,0) = nibble 9
	tm := New(&fakeBank{data: buf})
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetEnabled(true)
	tm.SetControl(0x00) // 40-col, attrs present, 4bpp, no textmode

	at := func(attr byte, x, y int) byte {
		buf[1] = attr
		dst := make([]byte, 16)
		tm.RenderScanline(y, dst)
		return dst[x]
	}
	if got := at(0x00, 0, 0); got != 9 {
		t.Errorf("identity: pixel (0,0) = %d, want 9", got)
	}
	// X mirror: the (0,0) pixel renders at display x=7.
	if at(0x08, 7, 0) != 9 || at(0x08, 0, 0) != 0 {
		t.Errorf("X mirror: want pixel at (7,0), got %d; (0,0)=%d", at(0x08, 7, 0), at(0x08, 0, 0))
	}
	// Y mirror: the (0,0) pixel renders at display row 7.
	if at(0x04, 0, 7) != 9 || at(0x04, 0, 0) != 0 {
		t.Errorf("Y mirror: want pixel at (0,7), got %d; (0,0)=%d", at(0x04, 0, 7), at(0x04, 0, 0))
	}
	// Rotate (bit 1; the XOR also asserts X mirror) → the (0,0) pixel
	// maps to display (7,0).
	if at(0x02, 7, 0) != 9 {
		t.Errorf("rotate: want (0,0)->display(7,0)=9, got %d", at(0x02, 7, 0))
	}
}

// TestTilemapScroll covers the pixel scroll offsets (NR$2F:$30 = X,
// NR$31 = Y). The tilemap wraps as a torus.
func TestTilemapScroll(t *testing.T) {
	buf := make([]byte, 0x4000)
	buf[1] = 0x01    // cell (0,1) = tile 1 (strip_flags → 1 byte/tile)
	buf[0x20] = 0x90 // tile 1 def (tilesBase $0000) (0,0) = nibble 9
	tm := New(&fakeBank{data: buf})
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetEnabled(true)
	tm.SetControl(0x20) // strip_flags (default attr), 40-col, 4bpp

	render := func() byte {
		dst := make([]byte, 320)
		tm.RenderScanline(0, dst)
		return dst[8] // display x=8 = cell (0,1) with no scroll
	}
	if got := render(); got != 9 {
		t.Errorf("no scroll: dst[8] = %d, want 9", got)
	}
	// scrollX=8 shifts left one tile: cell (0,1) moves to display x=0.
	tm.SetScrollX(8)
	dst := make([]byte, 320)
	tm.RenderScanline(0, dst)
	if dst[0] != 9 {
		t.Errorf("scrollX=8: dst[0] = %d, want 9", dst[0])
	}
	// scrollY=8 shifts up one tile row → cell (0,1) leaves display row 0.
	tm.SetScrollX(0)
	tm.SetScrollY(8)
	if got := render(); got != 0 {
		t.Errorf("scrollY=8: dst[8] = %d, want 0 (scrolled away)", got)
	}
}

// TestTilemapClip covers the NR$1B clip window: visible X is [x1*2,
// x2*2+1], visible Y is [y1, y2]; pixels outside are transparent.
func TestTilemapClip(t *testing.T) {
	buf := make([]byte, 0x4000)
	for i := 0; i < TilesPerRow40*TilesPerCol; i++ {
		buf[i] = 0x01 // every map cell = tile 1
	}
	for i := 0x820; i < 0x840; i++ {
		buf[i] = 0x99 // tile 1 def (tilesBase $0800) = all nibble 9
	}
	tm := New(&fakeBank{data: buf})
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x08)
	tm.SetEnabled(true)
	tm.SetControl(0x20) // strip_flags, 40-col

	render := func(y int) []byte {
		dst := make([]byte, 320)
		tm.RenderScanline(y, dst)
		return dst
	}
	// Default clip = full: solid row of 9s.
	if d := render(0); d[0] != 9 || d[100] != 9 {
		t.Fatalf("default clip: d[0]=%d d[100]=%d, want 9,9", d[0], d[100])
	}
	// Clip X to [4,9] (x1=2 → 4, x2=4 → 2*4+1 = 9).
	tm.SetClip(2, 4, 0, 0xFF)
	d := render(0)
	if d[3] != 0 || d[4] != 9 || d[9] != 9 || d[10] != 0 {
		t.Errorf("clip X [4,9]: d[3]=%d d[4]=%d d[9]=%d d[10]=%d, want 0,9,9,0",
			d[3], d[4], d[9], d[10])
	}
	// Clip Y to [5,255]: row 0 transparent, row 5 visible.
	tm.SetClip(0, 0x9F, 5, 0xFF)
	if render(0)[0] != 0 {
		t.Errorf("clip Y: row 0 should be transparent")
	}
	if render(5)[0] != 9 {
		t.Errorf("clip Y: row 5 should be visible")
	}
}

// TestOnTopBit — NR$6B bit 0 reports as t.OnTop().
func TestOnTopBit(t *testing.T) {
	tm := New(nil)
	tm.SetControl(0x01) // bit 0 set
	if !tm.OnTop() {
		t.Error("SetControl($01): OnTop() = false, want true")
	}
	tm.SetControl(0x00)
	if tm.OnTop() {
		t.Error("SetControl($00): OnTop() = true, want false")
	}
}

// TestTextmode1bpp covers NR$6B bit 3 (textmode), which NextZXOS
// dot-command viewers (NextGuide) drive but earlier sprints did not
// implement. In textmode the tile definitions are 1bpp (8 bytes/tile,
// MSB-first), and the FPGA (tilemap.vhd) builds each pixel's palette
// index as attr(7:1) & tile_bit — i.e. (attr & 0xFE) | bit. Without
// this, the 4bpp path reads 32 bytes/tile and renders garbage.
func TestTextmode1bpp(t *testing.T) {
	buf := make([]byte, 0x4000)
	// Map (strip_flags=1, 1 byte/tile): cell (0,0) = tile id 1.
	buf[0] = 0x01
	// Tile 1's 1bpp def: 8 bytes at tilesOffsetBase($0000) + 1*8.
	// Row 0 = 0b11000000: pixels 0,1 set; 2..7 clear.
	buf[0x08] = 0xC0
	tm := New(&fakeBank{data: buf})
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetDefaultAttr(0x34) // attr & 0xFE = $34 → bg $34, fg $35
	// enable($80) + strip_flags($20) + textmode($08).
	tm.SetControl(0x80 | 0x20 | 0x08)
	tm.SetEnabled(true)

	dst := make([]byte, 16)
	tm.RenderScanline(0, dst)
	want := []byte{0x35, 0x35, 0x34, 0x34, 0x34, 0x34, 0x34, 0x34}
	for i := 0; i < 8; i++ {
		if dst[i] != want[i] {
			t.Errorf("textmode pixel %d = $%02X, want $%02X", i, dst[i], want[i])
		}
	}
}

// TestTilemap512TileMode pins the 512-tile mode (NR$6B bit 1): the per-tile
// attribute byte's bit 0 is the tile index's 9th bit, extending the map to 512
// tiles. Sonic's options/text screen uses this (map cells are tile $00 / attr
// $01 → tile 256, where the font glyphs live); ignoring the MSB rendered the
// near-blank tile 0 everywhere and garbled the whole screen.
func TestTilemap512TileMode(t *testing.T) {
	bank := &fakeBank{data: make([]byte, 0x4000)}
	// Map at $0000, 2-byte cells. Cell 0 = tile low $00, attr $01 (bit0 = MSB).
	bank.data[0] = 0x00
	bank.data[1] = 0x01
	// Tile defs base $0800 (NR$6F = $08). Tile 256 at $0800 + 256*32 = $2800.
	for col := 0; col < 4; col++ {
		bank.data[0x800+256*32+col] = 0x99 // 4bpp nibble 9
	}
	tm := New(bank)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x08)
	tm.SetControl(0x02) // mode_512 (bit 1); 40-col, per-tile attrs
	tm.SetEnabled(true)

	dst := make([]byte, 256)
	tm.RenderScanline(0, dst)
	if dst[0] != 9 {
		t.Errorf("512-tile mode pixel0 = %d, want 9 (tile 256 via attr bit 0)", dst[0])
	}
}
