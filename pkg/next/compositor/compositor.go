package compositor

import (
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// Width is the per-scanline pixel count for the inner 256-wide screen
// pass. Layer 2's 320/640-pixel hi-res modes are composited separately
// through the wide paths (ComposeWideLayer2Row etc).
const Width = 256

// FullWidth is the per-scanline pixel count for full-screen passes
// that include the 32-px left/right border on each side of the
// classic 256-wide screen. Tilemap content in NextZXOS Browser
// extends across this full width.
const FullWidth = 320

// BorderOffsetX is the X offset within FullWidth where the inner
// 256-wide screen begins (matching ULA's BorderLeft). It is also the
// frame X coordinate of the paper's left edge for the sprite layer.
const BorderOffsetX = 32

// SpriteFrameYTop is the frame Y coordinate (sprites.vhd vcounter) of the
// paper's top edge: the central 256x192 paper sits at rows 32..223 of the
// 320x256 sprite/Layer-frame, so paper row N is frame row N+32. The sprite
// engine works in these frame coordinates (its X/Y attributes and its
// over-border clip are frame-relative), so the compositor adds this offset
// when it asks the sprite layer for a paper row.
const SpriteFrameYTop = 32

// DefaultTransparency is the Next-default 8-bit palette-mapped colour
// (NextReg 0x14 reset value) used as the global transparency colour for
// Layer 2 and the sprite layer until a guest writes NextReg 0x14 / 0x4B.
const DefaultTransparency byte = 0xE3

// PriorityMode is the decoded layer-ordering selector (NextReg 0x15 bits
// 4:2). Mirrors the same constants in pkg/next but redeclared here to
// avoid the compositor importing the umbrella package.
type PriorityMode byte

const (
	ModeSLU PriorityMode = 0 // Sprites over Layer 2 over ULA (reset default)
	ModeLSU PriorityMode = 1 // Layer 2 over Sprites over ULA
	ModeSUL PriorityMode = 2 // Sprites over ULA over Layer 2
	ModeLUS PriorityMode = 3 // Layer 2 over ULA over Sprites
	ModeUSL PriorityMode = 4 // ULA over Sprites over Layer 2
	ModeULS PriorityMode = 5 // ULA over Layer 2 over Sprites
	// ModeBlend and ModeBlend5 are the two additive blend orderings: the
	// Layer 2 pixel is summed with the ULA pixel per 3-bit channel, either
	// clamped at 7 (ModeBlend) or with the hardware's minus-5 curve applied
	// (ModeBlend5). See blendChannel / blend5Channel.
	ModeBlend  PriorityMode = 6
	ModeBlend5 PriorityMode = 7
)

// expand3 widens a 3-bit hardware channel to a full 8-bit one using the same
// bit-replication palette.RGB uses ('abc' becomes 'abcabcab').
func expand3(v byte) byte { return (v << 5) | (v << 2) | (v >> 1) }

// blendChannel sums two 3-bit channels and clamps at 7 — the ModeBlend
// arithmetic from zxnext.vhd:7196-7355 (Mix() case 6).
func blendChannel(a, b byte) byte {
	if v := int(a) + int(b); v <= 7 {
		return byte(v)
	}
	return 7
}

// blend5Channel sums two 3-bit channels and applies the hardware's minus-5
// curve (Mix() case 7): sums of 4 or less floor to 0, sums of 12 or more
// saturate to 7, and everything between loses 5.
func blend5Channel(a, b byte) byte {
	v := int(a) + int(b)
	switch {
	case v <= 4:
		return 0
	case (v>>2)&3 == 3: // >= 12
		return 7
	default:
		return byte((v + 11) & 0x0F)
	}
}

// PrioritySource is the contract for reading the active priority
// mode. pkg/next.LayerPriority satisfies it via Mode().
type PrioritySource interface {
	Mode() PriorityMode
}

// Compositor combines per-scanline output from Layer 2 (palette
// indices), the sprite engine (palette indices), and the ULA
// (already-rendered RGBA bytes) into a single RGBA output
// scanline.
//
// The active layer-priority mode is read from prioritySource on
// every call so guest writes to NextReg 0x15 take effect on the
// next composited row.
type Compositor struct {
	pal            *palette.Bank
	l2             *layer2.Layer2
	sprites        *sprite.Engine
	tilemap        *tilemap.Tilemap
	prioritySource PrioritySource
	transparency   byte
	spriteTrans    byte    // sprite transparency index (NextReg 0x4B)
	tilemapTrans   byte    // tilemap transparency nibble (NextReg 0x4C, low 4 bits)
	fallback       [4]byte // NR$4A fallback RGBA, shown when every layer is transparent
	blendMode      byte    // NextReg 0x68 bits 6:5 — blend colour source
	stencilMode    bool    // NextReg 0x68 bit 0 — ULA/tilemap stencil

	// Pre-allocated per-scanline scratch buffers — hoisted out
	// of ComposeScanline so 192 calls per frame don't churn the
	// allocator. Single goroutine assumption: the ULA's render
	// loop is the only caller.
	// Every layer is held as a frame row at its native width: 320, or 640
	// for the 640x256 Layer 2 and the 80-column tilemap.
	l2Scratch      [2 * FullWidth]byte
	l2Enabled      [2 * FullWidth]byte // per-pixel layer2_en plane for the same row
	spriteScratch  [FullWidth]byte     // sprite X/Y are frame-relative; paper starts at 32,32
	tilemapScratch [2 * FullWidth]byte
	tilemapEn      [2 * FullWidth]byte // per-pixel pixel_en_s plane for the same row
	tilemapBelow   [2 * FullWidth]byte // per-pixel pixel_below_o plane for the same row

	// cache records what the scratch buffers above hold, so a Copper write
	// that cannot change any layer's render re-composes the row without
	// rendering the layers again. See ComposeFrameRowTail.
	cache layerCache
	// renders counts layer renders, for tests that check the cache is used.
	renders int
}

// layerCache names the frame row whose layer renders the scratch buffers hold
// and which layers were rendered into them.
type layerCache struct {
	valid           bool
	fy              int
	l2, tm, sprites bool
}

// SetTilemap attaches the tilemap layer (Layer 3). nil unhooks.
// Compositor uses the tilemap's "on_top" bit (NR$6B bit 0) to
// decide whether to paint it after every other layer or under
// ULA; for now we honour on_top by painting it last when set.
func (c *Compositor) SetTilemap(t *tilemap.Tilemap) { c.tilemap = t }

// l2Transparent reports whether a Layer 2 pixel at palette index idx is
// transparent. Real hardware compares the PALETTE-MAPPED 8-bit colour (the 8
// MSB of the 9-bit Layer-2 palette entry) against the global transparency
// colour NR$14 — NOT the raw index. Sonic confirms this: it clears Layer 2 to
// index 0 but loads pal[0] → $13 with NR$14 = $13, so cleared (index-0) areas
// map to the transparency colour and let the tilemap show through. Comparing
// the raw index (0 != $13) wrongly painted Layer 2 opaque over the level.
func (c *Compositor) l2Transparent(l2Pal *palette.Palette, idx byte) bool {
	return byte(l2Pal.Get(idx)>>1) == c.transparency
}

// HiResLayer2Active reports whether Layer 2 is enabled in a hi-res mode
// (NR$70 resolution 1 or 2), so the display path drives the wide L2 pass.
func (c *Compositor) HiResLayer2Active() bool {
	return c.l2 != nil && c.l2.Enabled() && c.pal != nil && c.l2.Resolution() != 0
}

// HasActiveSprites reports whether the sprite layer is wired and enabled, so
// the ULA knows whether to return the full 256-line frame.
func (c *Compositor) HasActiveSprites() bool {
	return c.sprites != nil && c.sprites.Enabled() && c.pal != nil
}

// New returns a compositor that reads Layer 2 through the given
// palette bank and Layer 2 reference. Transparency defaults to
// DefaultTransparency. Any parameter may be nil for tests that
// only want to exercise the pass-through path; the sprite layer
// is wired separately via SetSprites so existing callers don't
// have to update on the Sprint-6 -> Sprint-7 transition.
func New(pal *palette.Bank, l2 *layer2.Layer2) *Compositor {
	return &Compositor{pal: pal, l2: l2, transparency: DefaultTransparency, tilemapTrans: 0x0F, spriteTrans: DefaultTransparency}
}

// SetSprites attaches the sprite engine. nil unhooks (compositor
// falls back to Layer-2-over-ULA only).
func (c *Compositor) SetSprites(s *sprite.Engine) { c.sprites = s }

// SetPrioritySource installs the LayerPriority reader. Without one
// the compositor defaults to ModeSLU (Sprites over Layer 2 over ULA).
func (c *Compositor) SetPrioritySource(p PrioritySource) { c.prioritySource = p }

// SetSpriteTransparency installs the palette index treated as
// transparent for the sprite layer (NextReg 0x4B). Defaults to
// DefaultTransparency ($E3, the FPGA reset value).
func (c *Compositor) SetSpriteTransparency(idx byte) { c.spriteTrans = idx }

// SetBlendMode installs NextReg 0x68 bits 6:5, which choose WHICH layer
// supplies the colour summed with Layer 2 in the two blend orderings
// (zxnext.vhd:5446; the mix_rgb selection is at 7140-7180):
//
//	00 = the ULA pixel        10 = nothing (no blending)
//	01 = the combined ULA+TM  11 = the tilemap pixel
//
// Reset value is 00, which is all that used to be modelled.
func (c *Compositor) SetBlendMode(m byte) { c.blendMode = m & 0x03 }

// BlendMode returns the active NR$68 blend selection.
func (c *Compositor) BlendMode() byte { return c.blendMode }

// SetStencilMode installs NextReg 0x68 bit 0. When set, and both the ULA and
// the tilemap are enabled, the two combine into a stencil (their colours
// ANDed) rather than layering (zxnext.vhd:7125-7138).
func (c *Compositor) SetStencilMode(on bool) { c.stencilMode = on }

// StencilMode reports the active NR$68 stencil selection.
func (c *Compositor) StencilMode() bool { return c.stencilMode }

// SetTilemapTransparency installs the tilemap transparency nibble
// (NextReg 0x4C, low 4 bits). A tilemap pixel is transparent when the
// low nibble of its palette index equals this value (FPGA
// tilemap.vhd:427). Defaults to 0x0F; NextZXOS dot commands (e.g.
// NextGuide) set it to other values, so wiring the live NR$4C is what
// keeps their text opaque instead of letting the ULA bleed through.
func (c *Compositor) SetTilemapTransparency(nibble byte) { c.tilemapTrans = nibble & 0x0F }

// SetTransparency installs the palette index that should be
// treated as "see-through" for the Layer 2 layer.
func (c *Compositor) SetTransparency(idx byte) {
	c.transparency = idx
}

// SetFallbackColour installs the NR$4A fallback RGBA — shown where every
// layer is transparent at a pixel.
func (c *Compositor) SetFallbackColour(r, g, b byte) {
	c.fallback = [4]byte{r, g, b, 0xFF}
}

// FallbackRGBA returns the NR$4A fallback colour. Used by the ULA when its
// output is disabled (NR$68 bit 7) to fill the frame with the fallback so the
// lower layers / fallback show instead of stale screen RAM.
func (c *Compositor) FallbackRGBA() [4]byte { return c.fallback }

// Transparency returns the currently-installed transparency index.
func (c *Compositor) Transparency() byte { return c.transparency }

// ComposeScanline writes 256 composited RGBA pixels (1024 bytes)
// to dst, given the ULA's already-rendered RGBA scanline (also
// 1024 bytes) for row y. The compositor fetches Layer 2's, the
// sprite engine's, and the tilemap's row y internally via their
// respective references, so callers don't need to coordinate the
// layer-by-layer fetch order.
//
// If every layer is nil/disabled, dst is copied straight from the ULA
// scanline. Otherwise the layers are combined per pixel according to
// the active NextReg 0x15 priority mode (see the ModeSLU/LSU/SUL/LUS
// cases below).
//
// dst must have at least Width*4 bytes; extra bytes are not
// touched.
// ComposeScanline composes the whole 256-pixel row.
func (c *Compositor) ComposeScanline(y int, ulaRGBA []byte, dst []byte) {
	c.ComposeScanlineRange(y, ulaRGBA, dst, 0, Width)
}

// ComposeScanlineRange composes pixels [x0, x1) of the row.
//
// The range exists so the caller can interleave Copper steps with
// compositing. The Copper's write pulse lands on a single 28 MHz clock
// (copper.vhd:102-104), a quarter of a pixel, and a MOVE retires every half
// pixel, so its writes fall wherever they fall across the line and not on any
// grid. The caller therefore composes the row once and re-composes its tail
// from the exact pixel each write landed in. An earlier note here claimed the
// Copper resolved to 8 pixels because its WAIT column field is in 8-pixel
// units; that is the resolution of a WAIT's release, not of a MOVE's effect.
//
// x0 and x1 must satisfy 0 <= x0 <= x1 <= Width: the paint loop below indexes
// dst at x*4 without checking, so a range outside the row is an out-of-range
// panic and not a clipped draw.
func (c *Compositor) ComposeScanlineRange(y int, ulaRGBA []byte, dst []byte, x0, x1 int) {
	if len(dst) < Width*4 {
		return
	}
	c.composeFrame(y+SpriteFrameYTop, ulaRGBA, dst, BorderOffsetX, x0+BorderOffsetX, x1+BorderOffsetX, 1, false, nil)
}

// ComposeFrameRowRange composes output pixels [x0, x1) of frame row fy (0..255
// of the 320x256 frame, paper rows at 32..223). ula holds the ULA layer for
// the row, border included, as RGBA; dst receives the result. Both are
// FullWidth*scale pixels: scale is 1 for a 320-pixel row and 2 for a 640-pixel
// row, the width the 80-column tilemap and the 640x256 Layer 2 need. Every
// layer is sampled at its own resolution and the NR$15 order is applied per
// pixel, as the FPGA's single pixel pipeline does, border included.
func (c *Compositor) ComposeFrameRowRange(fy int, ula, dst []byte, scale, x0, x1 int) {
	if scale != 2 {
		scale = 1
	}
	n := FullWidth * scale
	if len(ula) < n*4 || len(dst) < n*4 || x0 < 0 || x1 > n || x0 >= x1 {
		return
	}
	c.composeFrame(fy, ula, dst, 0, x0, x1, scale, false, nil)
}

// ComposeFrameRowTail is ComposeFrameRowRange for a row already composed once:
// it reuses the Layer 2, tilemap and sprite renders made for frame row fy by
// the last compose, unless InvalidateLayerRenders was called since. The paint
// stage always runs on live state, so palette entries, NR$14, NR$15's order,
// NR$4A, NR$4B, NR$4C and NR$68 take effect from x0 without a render.
//
// The caller re-composes a row's tail after each Copper write and must call
// InvalidateLayerRenders first when the write named a register for which
// LayerRenderReads is true.
func (c *Compositor) ComposeFrameRowTail(fy int, ula, dst []byte, scale, x0, x1 int) {
	if scale != 2 {
		scale = 1
	}
	n := FullWidth * scale
	if len(ula) < n*4 || len(dst) < n*4 || x0 < 0 || x1 > n || x0 >= x1 {
		return
	}
	c.composeFrame(fy, ula, dst, 0, x0, x1, scale, true, nil)
}

// LayerRenderReads is the package function of the same name, as a method so
// the ULA can reach it through an interface.
func (c *Compositor) LayerRenderReads(reg byte) bool { return LayerRenderReads(reg) }

// InvalidateLayerRenders drops the layer renders ComposeFrameRowTail would
// otherwise reuse.
func (c *Compositor) InvalidateLayerRenders() { c.cache.valid = false }

// LayerRenderReads reports whether a write to NextReg reg can change what a
// layer's scanline render produces, as opposed to how the paint stage colours
// and orders it. These are the registers that drop ComposeFrameRowTail's
// cache. Per layer:
//
//   - Layer 2 (layer2.RenderScanlineEnabled): NR$12 the active bank, NR$16 and
//     NR$71 bit 0 the X scroll, NR$17 the Y scroll, NR$18 the clip window,
//     NR$70 the resolution and palette offset.
//   - The tilemap (tilemap.RenderScanlineFlags): NR$1B the clip window, NR$2F,
//     NR$30 and NR$31 the scroll, NR$6B the enable and control bits, NR$6C the
//     default attribute, NR$6E and NR$6F the map and definition bases.
//   - Sprites (sprite.Engine.RenderScanline): NR$15 bits 0, 1, 5 and 6 (enable,
//     over border, border clip, zero on top), NR$19 the clip window, NR$35 to
//     NR$39 and NR$75 to NR$79 the attribute bytes.
//   - NR$02 and NR$03, which can reset or reconfigure every layer at once.
//
// Not in the set, and why: NR$69's only bit a render reads is bit 7, the
// Layer 2 enable, and which layers are enabled is part of the cache key, so
// turning a layer on or off re-renders without the register being listed (the
// same holds for the enable bits of NR$15 and NR$6B); NR$1C only resets the
// clip write indices; NR$34
// selects the sprite later attribute writes go to without changing any
// sprite; NR$14, NR$40 to NR$44, NR$4A, NR$4B, NR$4C and NR$68 are read by the
// paint stage, which always runs live. Pattern RAM and the layers' memory are
// not reachable from the Copper.
//
// TestLayerCacheMatchesUncachedForEveryCopperRegister (cmd/zx_go) renders a
// frame with and without the cache for every register the Copper can name and
// fails on any difference, so a register missing here is caught there.
func LayerRenderReads(reg byte) bool {
	switch reg {
	case 0x02, 0x03,
		0x12, 0x16, 0x17, 0x18, 0x70, 0x71,
		0x1B, 0x2F, 0x30, 0x31, 0x6B, 0x6C, 0x6E, 0x6F,
		0x15, 0x19, 0x35, 0x36, 0x37, 0x38, 0x39, 0x75, 0x76, 0x77, 0x78, 0x79:
		return true
	}
	return false
}

// WideScale is 2 when a layer needs a 640-pixel row, the 80-column tilemap or
// the 640x256 Layer 2, and 1 otherwise.
func (c *Compositor) WideScale() int {
	if c.pal == nil {
		return 1
	}
	if c.tilemap != nil && c.tilemap.Enabled() && c.tilemap.Is80Col() {
		return 2
	}
	if c.l2 != nil && c.l2.Enabled() && c.l2.Resolution() == 2 {
		return 2
	}
	return 1
}

// composeFrame is the pixel pipeline. Output pixel ox runs over [x0, x1) in a
// row FullWidth*scale wide; ula and dst are indexed from origin*scale (the
// paper pass hands 256-pixel rows that start at frame column 32). Each layer
// is fetched once as a frame row in its native width and sampled at
// ox * width / (FullWidth*scale).
//
// reuse asks for the layer renders already in the scratch buffers to be used
// when they were made for this row and the same set of layers; otherwise every
// active layer is rendered afresh.
func (c *Compositor) composeFrame(fy int, ulaRGBA, dst []byte, origin, x0, x1, scale int, reuse bool, skip func(int) bool) {
	outW := FullWidth * scale
	base := origin * scale
	doL2 := c.l2 != nil && c.l2.Enabled() && c.pal != nil
	doSprites := c.sprites != nil && c.sprites.Enabled() && c.pal != nil
	doTilemap := c.tilemap != nil && c.tilemap.Enabled() && c.pal != nil
	if !doL2 && !doSprites && !doTilemap {
		c.cache.valid = false
		// Bounded like the paint loop below: this is still a range compose, and
		// the caller re-composes only the tail after a Copper write. Copying the
		// whole row here would erase the head, which was composed under the
		// layer state in force before the write turned those layers off.
		if &ulaRGBA[0] != &dst[0] {
			for ox := x0; ox < x1; ox++ {
				if skip != nil && skip(ox) {
					continue
				}
				o := (ox - base) * 4
				copy(dst[o:o+4], ulaRGBA[o:o+4])
			}
		}
		return
	}

	// The active layers' palettes. PaletteForLayer reads the per-layer
	// "first/second" selection (NextReg 0x43 bits 4-7) independently of the
	// write-target bits. Sprint 6's mistake was reading Selected() (the write
	// target) and assuming it meant the active palette.
	//
	// Per FPGA zxnext.vhd:6981 the tilemap pixel address has bit 9 = 1, bit 8
	// = nr_6b bit 4 (tm_palette_select), bits 7:0 = tile pixel. The FPGA
	// palette SRAM is 4 logical banks of 256 entries: address "00" = ULA
	// First, "01" = ULA Second, "10" = Tilemap First, "11" = Tilemap Second.
	// Our Zeus-aligned 8-palette enum keeps the same physical SRAM banks at
	// indices 0,1 (ULA) and 6,7 (Tilemap), so the natural mapping is
	// palettes[6 + activeTilemap].
	var l2Pal, tilemapPal, spritePal *palette.Palette
	if doL2 {
		l2Pal = c.pal.PaletteForLayer(palette.LayerLayer2)
		doL2 = l2Pal != nil
	}
	if doTilemap {
		tilemapPal = c.pal.PaletteForLayer(palette.LayerTilemap)
		doTilemap = tilemapPal != nil
	}
	if doSprites {
		spritePal = c.pal.PaletteForLayer(palette.LayerSprites)
		doSprites = spritePal != nil
	}
	want := layerCache{valid: true, fy: fy, l2: doL2, tm: doTilemap, sprites: doSprites}
	render := !reuse || c.cache != want
	if render {
		c.renders++
	}
	c.cache = want

	// The active layers' scanlines, rendered unless the scratch buffers
	// already hold this row's.
	var l2Scanline []byte
	var l2Enabled []byte
	l2W := FullWidth
	if doL2 {
		l2Scanline = c.l2Scratch[:]
		l2Enabled = c.l2Enabled[:]
		if c.l2.Resolution() == 0 {
			// 256x192: the paper only, addressed by paper row.
			l2W = FullWidth
			if render {
				clear(l2Enabled)
				if py := fy - SpriteFrameYTop; py >= 0 && py < 192 {
					c.l2.RenderScanlineEnabled(py, l2Scanline[BorderOffsetX:BorderOffsetX+Width], l2Enabled[BorderOffsetX:BorderOffsetX+Width])
				}
			}
		} else {
			// 320x256 / 640x256: the frame row (wide counters).
			l2W = c.l2.LineWidth()
			if render {
				clear(l2Enabled)
				c.l2.RenderScanlineEnabled(fy, l2Scanline[:l2W], l2Enabled[:l2W])
			}
		}
	}
	var tilemapScan []byte
	var tilemapEn []byte
	var tilemapBelow []byte
	tmW := FullWidth
	if doTilemap {
		// The tilemap is a frame-row layer, 320 pixels wide or 640 in
		// 80-column mode, addressed vertically by the wide counter wvc,
		// 0 thirty-two lines above the paper (zxnext.vhd:4390,
		// zxula_timing.vhd:477-499).
		if c.tilemap.Is80Col() {
			tmW = 2 * FullWidth
		}
		if render {
			c.tilemap.RenderScanlineFlags(fy, c.tilemapScratch[:tmW], c.tilemapEn[:tmW], c.tilemapBelow[:tmW])
		}
		tilemapScan = c.tilemapScratch[:tmW]
		tilemapEn = c.tilemapEn[:tmW]
		tilemapBelow = c.tilemapBelow[:tmW]
	}
	var spriteScan []byte
	if doSprites {
		spriteScan = c.spriteScratch[:]
		if render {
			// Zero the buffer; the sprite engine only OVERWRITES
			// pixels it covers, leaving previous content otherwise.
			clear(spriteScan)
			// Sprites live in frame coordinates (320x256, paper at 32,32).
			c.sprites.RenderScanline(fy, spriteScan, FullWidth)
		}
	}

	// Sample indices: each layer is held at its own width.
	l2i := func(ox int) int { return ox * l2W / outW }
	tmi := func(ox int) int { return ox * tmW / outW }
	spi := func(ox int) int { return ox * FullWidth / outW }

	// Decode the active priority mode. Each mode dictates the
	// painting order top-down: the LAST one written wins per pixel.
	// We always paint background-to-foreground.
	mode := ModeSLU
	if c.prioritySource != nil {
		mode = c.prioritySource.Mode()
	}
	// paintULA / paintL2 / paintSprites are functors that, given
	// the dst offset, overlay that layer's pixel if non-transparent.
	paintULA := func(off int) {
		dst[off+0] = ulaRGBA[off+0]
		dst[off+1] = ulaRGBA[off+1]
		dst[off+2] = ulaRGBA[off+2]
		dst[off+3] = ulaRGBA[off+3]
	}
	// ulaTransparentAt reports whether the ULA pixel at off is transparent:
	// its colour's RGB332, the top 8 bits of the 9-bit palette entry it came
	// from, equals NR$14 (zxnext.vhd:7100). The ULA arrives expanded from
	// the palette, so each channel's top bits are the entry's bits.
	ulaTransparentAt := func(off int) bool {
		v := ulaRGBA[off+0]>>5<<5 | ulaRGBA[off+1]>>5<<2 | ulaRGBA[off+2]>>6
		return v == c.transparency
	}
	// paintBase lays the ULA down as the bottom layer, substituting the
	// NR$4A fallback colour where the ULA pixel is transparent — so a
	// pixel left transparent by every layer ends up showing the fallback.
	paintBase := func(off int) {
		if ulaTransparentAt(off) {
			dst[off+0], dst[off+1], dst[off+2], dst[off+3] = c.fallback[0], c.fallback[1], c.fallback[2], c.fallback[3]
			return
		}
		paintULA(off)
	}
	// paintULAStencil paints the ULA but skips transparent pixels — used
	// for the SUL re-paint so Layer 2 shows through a transparent ULA.
	paintULAStencil := func(off int) {
		if ulaTransparentAt(off) {
			return
		}
		paintULA(off)
	}
	// l2Off reports that Layer 2 contributes nothing at column x: the
	// layer is disabled, or the FPGA's per-pixel layer2_en is clear —
	// outside the NR$18 clip window, or off-screen. A zero palette index
	// is a real colour, so this cannot be inferred from l2Scanline: doing
	// so painted opaque black over the layers beneath wherever a program
	// clipped Layer 2.
	l2Off := func(ox int) bool {
		x := l2i(ox)
		return !doL2 || x >= len(l2Enabled) || l2Enabled[x] == 0
	}
	paintL2 := func(off, x int) {
		if l2Off(x) {
			return
		}
		if idx := l2Scanline[l2i(x)]; !c.l2Transparent(l2Pal, idx) {
			r, g, b := l2Pal.RGB(idx)
			dst[off+0] = r
			dst[off+1] = g
			dst[off+2] = b
			dst[off+3] = 0xFF
		}
	}
	// paintL2Priority promotes a Layer 2 pixel whose palette entry carries
	// the priority bit (NR$44 bit 7) above the ULA+TM — used in SUL, where
	// Layer 2 normally sits below the ULA (zxnext.vhd:7123). A no-op when
	// the pixel is transparent or lacks the priority bit.
	paintL2Priority := func(off, x int) {
		if l2Off(x) {
			return
		}
		idx := l2Scanline[l2i(x)]
		if c.l2Transparent(l2Pal, idx) || !l2Pal.HasPriority(idx) {
			return
		}
		r, g, b := l2Pal.RGB(idx)
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
	paintSprites := func(off, x int) {
		if !doSprites {
			return
		}
		// x is the paper pixel (0..255); the sprite buffer is in frame
		// coordinates, so the paper's left edge is at BorderOffsetX. A pixel
		// is transparent when it is the 0 "uncovered" sentinel OR equals the
		// sprite transparency index (NR$4B, default $E3) — the latter is how
		// 8bpp sprites (e.g. Nextoid's bat/HUD) mark their see-through cells.
		if idx := spriteScan[spi(x)]; idx != 0 && idx != c.spriteTrans {
			r, g, b := spritePal.RGB(idx)
			dst[off+0] = r
			dst[off+1] = g
			dst[off+2] = b
			dst[off+3] = 0xFF
		}
	}
	// paintTilemap overlays the tilemap pixel atop the ULA layer.
	//
	// Per zxnext.vhd:7116, the FPGA mixes tilemap with ULA into a
	// single ulatm_rgb signal that feeds into the NR$15 priority
	// chain — Layer 2 and Sprites layer on top of (or below) the
	// combined ULA+TM result, NOT on top of the standalone tilemap.
	// So the testcard's logo / instruction text on Layer 2 must
	// remain visible through the tilemap's "background" tile pixels
	// (palette[0]) even when NR$6B bit 0 (tm_on_top) is set: tm_on_top
	// pushes tilemap above ULA, not above L2.
	//
	// Transparency rule from tilemap.vhd:427:
	//	pixel_en_standard_s = '1' when video_data(3:0) /= NR$4C(3:0)
	// — i.e. the LOW nibble of the final palette index gates pixel
	// visibility, not "index 0 = transparent". For Sprint N we model
	// this with NR$4C low-nibble == tile-nibble (idx low 4 bits) as
	// the transparency condition. Without on_top, the tilemap layer
	// also defers to nibble-0 transparency to let ULA show through —
	// the Sprint N pre-fix behaviour kept for legacy paths.
	tmOnTop := false
	if c.tilemap != nil {
		tmOnTop = c.tilemap.OnTop()
	}
	tmTransparentNibble := c.tilemapTrans
	paintTilemapOnULA := func(off, x int) {
		if !doTilemap {
			return
		}
		// pixel_en_s: the tilemap produced no pixel here at all
		// (outside the NR$1B window, or off the tilemap). A zero index
		// is a real colour, so this cannot be inferred from idx.
		x = tmi(x)
		if x >= len(tilemapEn) || tilemapEn[x] == 0 {
			return
		}
		idx := tilemapScan[x]
		// FPGA transparency: the LOW nibble of the palette index is
		// compared to NR$4C(3:0). When equal -> transparent regardless
		// of on_top. Otherwise the pixel is opaque AND covers ULA
		// only when tm_on_top is set OR the tile flag asks for it.
		if (idx & 0x0F) == tmTransparentNibble {
			return
		}
		// pixel_below_o (tilemap.vhd:388): when set, the ULA draws
		// over this tilemap pixel. This used to be approximated as
		// "on_top off and nibble 0", which hid every background tile
		// that carried a palette offset.
		if x < len(tilemapBelow) && tilemapBelow[x] != 0 {
			return
		}
		r, g, b := tilemapPal.RGB(idx)
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
	if mode == ModeBlend || mode == ModeBlend5 {
		// --- the two additive blend orderings (NR$15 = 110 / 111) ---
		//
		// These do not fit the "ULA at the bottom" chain: under NR$68's reset
		// blend selection the plain ULA layer never surfaces on its own
		// (zxnext.vhd:7140-7180 leaves mix_top / mix_bot on the tilemap), and
		// contributes only through the additive sum with Layer 2. A pixel with
		// nothing to show falls back to NR$4A rather than to the ULA.
		paintFallback := func(off int) {
			dst[off+0], dst[off+1] = c.fallback[0], c.fallback[1]
			dst[off+2], dst[off+3] = c.fallback[2], c.fallback[3]
		}
		// blendChannels3 returns the 3-bit channels of the colour NR$68 bits
		// 6:5 select as the blend source (Mix()'s mix_rgb, zxnext.vhd
		// 7140-7180). A transparent source contributes nothing, matching
		// mix_rgb being zero when mixRGBTransparent.
		//
		// The ULA arrives already expanded to RGBA; palette.RGB replicates
		// 3-bit 'abc' as 'abcabcab', so the top 3 bits are the original.
		ulaChannels3 := func(off int) (byte, byte, byte) {
			if ulaTransparentAt(off) {
				return 0, 0, 0
			}
			return ulaRGBA[off+0] >> 5, ulaRGBA[off+1] >> 5, ulaRGBA[off+2] >> 5
		}
		tmChannels3 := func(x int) (byte, byte, byte) {
			if !doTilemap {
				return 0, 0, 0
			}
			idx := tilemapScan[tmi(x)]
			if (idx & 0x0F) == tmTransparentNibble {
				return 0, 0, 0
			}
			v := tilemapPal.Get(idx)
			return byte((v >> 6) & 7), byte((v >> 3) & 7), byte(v & 7)
		}
		blendChannels3 := func(off, x int) (byte, byte, byte) {
			switch c.blendMode {
			case 1: // the combined ULA+TM: the tilemap wins where it is opaque
				if tr, tg, tb := tmChannels3(x); tr|tg|tb != 0 {
					return tr, tg, tb
				}
				return ulaChannels3(off)
			case 2: // no blending
				return 0, 0, 0
			case 3: // the tilemap
				return tmChannels3(x)
			default: // 0: the ULA
				return ulaChannels3(off)
			}
		}
		paintBlend := func(off, x int) {
			if l2Off(x) {
				return
			}
			idx := l2Scanline[l2i(x)]
			if c.l2Transparent(l2Pal, idx) {
				return
			}
			v := l2Pal.Get(idx)
			lr, lg, lb := byte((v>>6)&7), byte((v>>3)&7), byte(v&7)
			ur, ug, ub := blendChannels3(off, x)
			mix := blendChannel
			if mode == ModeBlend5 {
				mix = blend5Channel
			}
			dst[off+0], dst[off+1] = expand3(mix(lr, ur)), expand3(mix(lg, ug))
			dst[off+2], dst[off+3] = expand3(mix(lb, ub)), 0xFF
		}
		paintBlendPriority := func(off, x int) {
			if l2Off(x) || !l2Pal.HasPriority(l2Scanline[l2i(x)]) {
				return
			}
			paintBlend(off, x)
		}
		// paintTilemapRaw applies the strict FPGA transparency rule
		// (tilemap.vhd:427) with no on_top gating — the blend chain expresses
		// above/below through paint ORDER instead.
		paintTilemapRaw := func(off, x int) {
			if !doTilemap {
				return
			}
			idx := tilemapScan[tmi(x)]
			if (idx & 0x0F) == tmTransparentNibble {
				return
			}
			r, g, b := tilemapPal.RGB(idx)
			dst[off+0], dst[off+1], dst[off+2], dst[off+3] = r, g, b, 0xFF
		}
		for x := x0; x < x1; x++ {
			if skip != nil && skip(x) {
				continue
			}
			off := (x - base) * 4
			paintFallback(off)
			paintBlend(off, x) // Layer 2 opaque -> the additive sum
			if !tmOnTop {
				paintTilemapRaw(off, x) // mix_bot
			}
			paintSprites(off, x)
			if tmOnTop {
				paintTilemapRaw(off, x) // mix_top
			}
			paintBlendPriority(off, x) // NR$44 priority bit wins outright
		}
		return
	}

	for x := x0; x < x1; x++ {
		if skip != nil && skip(x) {
			continue
		}
		off := (x - base) * 4
		// First lay down ULA, then mix the tilemap onto ULA (the
		// FPGA's ulatm_rgb formation), then the NR$15 priority chain
		// places Layer 2 / Sprites above or below the combined layer.
		paintBase(off)
		paintTilemapOnULA(off, x)
		switch mode {
		case ModeSLU: // Sprites over Layer 2 over ULA+TM
			paintL2(off, x)
			paintSprites(off, x)
		case ModeLSU: // Layer 2 over Sprites over ULA+TM
			paintSprites(off, x)
			paintL2(off, x)
		case ModeSUL: // Sprites over ULA+TM over Layer 2
			// Layer 2 sits below the combined ULA+TM layer: paint L2
			// first, then re-paint ULA+TM above it — but via the stencil
			// so a transparent ULA pixel lets Layer 2 show through (the
			// per-pixel SUL stencil; a no-op when ULA transparency is
			// inactive, i.e. the default NR$14 = $E3).
			paintL2(off, x)
			paintULAStencil(off)
			paintTilemapOnULA(off, x)
			paintL2Priority(off, x) // priority-bit L2 promoted above ULA+TM
			paintSprites(off, x)
		case ModeLUS: // Layer 2 over ULA+TM over Sprites
			paintSprites(off, x)
			paintULAStencil(off)
			paintTilemapOnULA(off, x)
			paintL2(off, x)
		case ModeUSL: // ULA+TM over Sprites over Layer 2
			// Mix() case 4 resolves top-down as
			// L2-priority > ULA+TM > sprites > Layer 2, so paint the
			// reverse of that.
			paintL2(off, x)
			paintSprites(off, x)
			paintULAStencil(off)
			paintTilemapOnULA(off, x)
			paintL2Priority(off, x)
		case ModeULS: // ULA+TM over Layer 2 over Sprites
			// Mix() case 5: L2-priority > ULA+TM > Layer 2 > sprites.
			// Only the sprite/Layer 2 order differs from USL.
			paintSprites(off, x)
			paintL2(off, x)
			paintULAStencil(off)
			paintTilemapOnULA(off, x)
			paintL2Priority(off, x)
		}
	}
}
