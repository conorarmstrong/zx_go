package tilemap

// BankReader is the minimum the tilemap needs from the memory bus:
// fetch one 16 KB RAM bank by index. pkg/memory.Memory's GetPage
// satisfies this — same contract as pkg/next/layer2.
type BankReader interface {
	GetPage(bank int) []byte
}

// Width / Height fix the visible-area pixel dimensions in the
// only mode this v1 supports (40x32 character cells, 8x8 pixels
// per tile = 320 wide). The compositor clips to ULA's 256 for now;
// the extra 64 pixels on each side are out of view.
const (
	TilesPerRow40 = 40
	TilesPerCol   = 32
	TileWidth     = 8
	TileHeight    = 8
	Width40       = TilesPerRow40 * TileWidth // 320
	Height        = TilesPerCol * TileHeight  // 256
)

// Tilemap is the Sprint-N Spectrum Next tilemap layer. Backed by
// one or two 16 KB Spectrum RAM banks (bank 5 by default; bank 7
// when NR$6E/$6F bit 7 is set). Per FPGA verilog
// (cores/zxnext/src/video/tilemap.vhd) the addressing is:
//
//	tile_map_base  ($6E)  — 7 bits: bit 7 = bank select, bits 5:0
//	                       = 256-byte offset within bank
//	tile_defs_base ($6F)  — same encoding as $6E
//	control         ($6B)  — bit 7 enable, bit 6 mode (0=40x32,
//	                        1=80x32), bit 5 strip_flags, bit 3
//	                        textmode, bit 1 mode_512, bit 0 on_top
//	default_attr    ($6C) — used when strip_flags=1
//
// Sprint N covers the path NextZXOS Browser exercises during boot:
// 40x32 mode with strip_flags=1 (no per-tile attribute), 4bpp tile
// definitions, no scroll, no clipping, no mirror/rotate. Extras
// are TODO; the structure leaves room.
type Tilemap struct {
	mem BankReader

	enabled     bool
	control     byte // mirror of NR$6B
	defaultAttr byte // NR$6C
	mapBase     byte // NR$6E
	tilesBase   byte // NR$6F
	scrollX     int  // NR$2F:$30 (10-bit pixel scroll X)
	scrollY     int  // NR$31 (8-bit pixel scroll Y)
	clipX1      byte // NR$1B clip window: visible X is [clipX1*2, clipX2*2+1]
	clipX2      byte
	clipY1      byte // visible Y is [clipY1, clipY2]
	clipY2      byte
}

// New constructs a tilemap reader. Disabled by default; clip window
// defaults to the full area ({0, 0x9F, 0, 0xFF} per the FPGA reset). The
// dispatcher writes NR$6B bit 7 to flip enabled on / off.
func New(mem BankReader) *Tilemap {
	return &Tilemap{mem: mem, clipX2: 0x9F, clipY2: 0xFF}
}

// SetEnabled toggles tilemap rendering (NR$6B bit 7).
func (t *Tilemap) SetEnabled(on bool) { t.enabled = on }

// Enabled reports current state.
func (t *Tilemap) Enabled() bool { return t.enabled }

// SetControl writes NR$6B low 7 bits (the enable bit is handled
// separately by SetEnabled, but in practice the wire layer
// decomposes the byte and calls both).
func (t *Tilemap) SetControl(v byte) { t.control = v & 0x7F }

// SetMode40 is a convenience for tests: clears the 80-col bit.
func (t *Tilemap) SetMode40() { t.control &^= 1 << 6 }

// SetMode80 is the dual.
func (t *Tilemap) SetMode80() { t.control |= 1 << 6 }

// SetStripFlags toggles NR$6B bit 5 — when set, the per-tile flag
// byte is omitted from the map and defaultAttr is used.
func (t *Tilemap) SetStripFlags(on bool) {
	if on {
		t.control |= 1 << 5
	} else {
		t.control &^= 1 << 5
	}
}

// SetDefaultAttr writes NR$6C (used when StripFlags is set).
func (t *Tilemap) SetDefaultAttr(v byte) { t.defaultAttr = v }

// SetTileMapBase writes NR$6E.
func (t *Tilemap) SetTileMapBase(v byte) { t.mapBase = v }

// SetTilesBase writes NR$6F.
func (t *Tilemap) SetTilesBase(v byte) { t.tilesBase = v }

// OnTop reports whether NR$6B bit 0 (tm_on_top) is set. When on,
// the FPGA renders the tilemap as ALWAYS opaque over every layer
// below — even nibble-0 pixels produce palette[0] rather than
// letting ULA / Layer 2 show through. The compositor uses this to
// switch its transparency check off so palette[0] is honoured (the
// testcard sets palette[0] = black + uses tile $01 = all-nibble-0
// as the "background" tile; without OnTop honoured, the inner
// screen ate the ULA garbage that sits in bank 5 alongside the
// tile data and showed solid black via the colour-zero attribute
// byte, instead of the testcard's intended palette colour).
func (t *Tilemap) OnTop() bool { return t.control&0x01 != 0 }

// Is80Col reports whether NR$6B bit 6 (80-column mode) is set. In that
// mode the tilemap is 80 tiles × 8px = 640 pixels wide, so the caller
// must render it through a 640-pixel buffer (the 320-pixel display path
// would only cover the left 40 columns).
func (t *Tilemap) Is80Col() bool { return t.control&(1<<6) != 0 }

// SetScrollX / SetScrollY set the tilemap pixel scroll offsets (NR$2F:$30
// = X, 10-bit; NR$31 = Y, 8-bit per FPGA nr_30_tm_scrollx/nr_31). The
// tilemap wraps as a torus.
func (t *Tilemap) SetScrollX(v int) { t.scrollX = v }
func (t *Tilemap) SetScrollY(v int) { t.scrollY = v }

// SetClip sets the tilemap clip window (NR$1B). Visible pixels are X in
// [x1*2, x2*2+1] (in 40-col 2-pixel units; the bounds double in 80-col so
// the default still covers the full 640) and Y in [y1, y2]. Per FPGA
// tilemap.vhd:416-419. The power-on default {0, 0x9F, 0, 0xFF} is the
// full tilemap.
func (t *Tilemap) SetClip(x1, x2, y1, y2 byte) {
	t.clipX1, t.clipX2, t.clipY1, t.clipY2 = x1, x2, y1, y2
}

// RenderScanline writes len(dst) palette-indexed bytes for visible
// row y. Caller controls dst length; we read at most that many
// pixels and stop. Out-of-mode / disabled returns all zeros so the
// compositor's transparency-or-replace path works correctly.
func (t *Tilemap) RenderScanline(y int, dst []byte) {
	for i := range dst {
		dst[i] = 0
	}
	if !t.enabled || t.mem == nil {
		return
	}
	if y < 0 || y >= Height {
		return
	}
	// Clip window (NR$1B): rows outside [clipY1, clipY2] are transparent.
	if y < int(t.clipY1) || y > int(t.clipY2) {
		return
	}

	// Bank select: bit 7 of NR$6E/$6F selects bank 7 (vs bank 5).
	//
	// The FPGA stores NR$6E as two pieces (zxnext.vhd 5468-5469):
	//
	//	nr_6e_tilemap_base_7 <= nr_wr_dat(7);
	//	nr_6e_tilemap_base   <= nr_wr_dat(5 downto 0);
	//
	// then concatenates them as `bank7 & offset6` to form the 7-bit
	// tm_map_base_i. Inside tilemap.vhd "bit 6 of tm_map_base" maps
	// back to the original CPU-write bit 7 — bit 6 of the CPU byte
	// is discarded entirely. NextZXOS Browser writes $40 expecting
	// bit 7=0 → bank 5 (the classic 7FFD screen page) with offset
	// bits 5:0 = 0 → base $0000 in bank 5.
	mapBank := 5
	if t.mapBase&0x80 != 0 {
		mapBank = 7
	}
	tilesBank := 5
	if t.tilesBase&0x80 != 0 {
		tilesBank = 7
	}
	mapBuf := t.mem.GetPage(mapBank)
	tilesBuf := t.mem.GetPage(tilesBank)
	if len(mapBuf) < 0x4000 || len(tilesBuf) < 0x4000 {
		return
	}

	mapOffsetBase := int(t.mapBase&0x3F) << 8
	tilesOffsetBase := int(t.tilesBase&0x3F) << 8

	// 40-col mode: 1 byte per tile (strip_flags=1) or 2 bytes
	// (strip_flags=0, tile + flag pair). Sprint N supports both —
	// the flag is currently ignored beyond reading it.
	tilesPerRow := TilesPerRow40
	if t.control&(1<<6) != 0 {
		tilesPerRow = 80
	}
	// Clip window X bounds (NR$1B): 2-pixel units in 40-col, doubled in
	// 80-col so the default {0, 0x9F} still spans the full width.
	xscale := 1
	if tilesPerRow == 80 {
		xscale = 2
	}
	clipXStart := int(t.clipX1) * 2 * xscale
	clipXEnd := (int(t.clipX2)*2+1)*xscale + (xscale - 1)
	stripFlags := t.control&(1<<5) != 0
	bytesPerTile := 2
	if stripFlags {
		bytesPerTile = 1
	}

	// Pixel scroll (NR$2F:$30 / $31): offset the source coords and wrap
	// the tilemap as a torus.
	absY := y + t.scrollY
	tileY := (absY / TileHeight) % TilesPerCol
	pixelRow := absY % TileHeight

	// Textmode (NR$6B bit 3): tile definitions are 1bpp (8 bytes/tile)
	// rather than 4bpp (32 bytes/tile). NextZXOS dot-command viewers
	// (e.g. NextGuide) use it for text.
	textmode := t.control&(1<<3) != 0
	// 512-tile mode (NR$6B bit 1): the per-tile attribute byte's bit 0 is
	// the tile index's 9th bit, extending the map from 256 to 512 tiles.
	// Sonic's options/text screen relies on this (its font lives in tiles
	// 256+, addressed via attr bit 0); ignoring it renders tile 0 everywhere.
	mode512 := t.control&(1<<1) != 0

	for x := 0; x < len(dst); x++ {
		if x < clipXStart || x > clipXEnd {
			continue // outside the clip window — leave transparent
		}
		absX := x + t.scrollX
		tileX := (absX / TileWidth) % tilesPerRow
		mapEntry := mapOffsetBase + (tileY*tilesPerRow+tileX)*bytesPerTile
		mapEntry &= 0x3FFF
		// Per-tile attribute, or the global default_attr when
		// strip_flags eliminates the attribute byte.
		attr := t.defaultAttr
		if !stripFlags {
			attr = mapBuf[(mapEntry+1)&0x3FFF]
		}
		tileIdx := int(mapBuf[mapEntry])
		if mode512 {
			tileIdx |= int(attr&0x01) << 8
		}
		pixelInTile := absX % TileWidth

		if textmode {
			// 1bpp tile: 8 bytes/tile, one byte per row, MSB-first.
			// The FPGA (tilemap.vhd) forms the pixel's palette index
			// as attr(7:1) & tile_bit — "extend palette offset to 7
			// bits" — i.e. (attr & 0xFE) | bit. Index 0 stays the
			// transparent slot so the compositor falls through to ULA.
			defAddr := (tilesOffsetBase + int(tileIdx)*8 + pixelRow) & 0x3FFF
			bit := (tilesBuf[defAddr] >> (7 - uint(pixelInTile))) & 1
			dst[x] = (attr & 0xFE) | bit
			continue
		}

		// Per-tile transform (FPGA tilemap.vhd:320-324): attr bit 3 = X
		// mirror, bit 2 = Y mirror, bit 1 = rotate (swaps X/Y, and a
		// rotation also inverts the X mirror). textmode (above) skips it.
		tx, ty := pixelInTile, pixelRow
		if (attr>>3)&1 != (attr>>1)&1 { // effective X mirror
			tx = (TileWidth - 1) - tx
		}
		if attr&0x04 != 0 { // Y mirror
			ty = (TileHeight - 1) - ty
		}
		if attr&0x02 != 0 { // rotate: exchange X and Y
			tx, ty = ty, tx
		}
		// Standard 4bpp tile defs: 32 bytes per tile (8 rows × 4
		// bytes); attr bits 7:4 are the palette offset, the pixel is a
		// nibble. ty selects the 4-byte row, tx the pixel within it.
		paletteOffset := (attr >> 4) & 0x0F
		tileDataBase := (tilesOffsetBase + int(tileIdx)*32 + ty*4) & 0x3FFF
		b := tilesBuf[tileDataBase+tx/2]
		var nibble byte
		if tx&1 == 0 {
			nibble = (b >> 4) & 0x0F
		} else {
			nibble = b & 0x0F
		}
		if nibble == 0 {
			// Index 0 is the standard transparent slot — pass through.
			dst[x] = 0
		} else {
			dst[x] = (paletteOffset << 4) | nibble
		}
	}
}
