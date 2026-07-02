package compositor

import (
	"image/color"

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
)

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

	// ULA transparency: the classic ULA renders via its own 16-colour
	// palette, so a ULA pixel is "transparent" (lets a lower layer show in
	// SUL, or the NR$4A fallback) when its RGBA equals ulaPalette[NR$14].
	// ulaTransActive is false when NR$14 >= 16 (no standard ULA colour can
	// match), making the whole feature a no-op for the default $E3.
	ulaPalette     [16]color.RGBA
	ulaPaletteSet  bool
	ulaTransActive bool
	ulaTransRGBA   [4]byte

	// Pre-allocated per-scanline scratch buffers — hoisted out
	// of ComposeScanline so 192 calls per frame don't churn the
	// allocator. Single goroutine assumption: the ULA's render
	// loop is the only caller.
	l2Scratch      [Width]byte
	spriteScratch  [FullWidth]byte // full 320-wide row in FRAME coordinates (sprite X/Y are frame-relative; paper starts at 32,32)
	tilemapScratch [FullWidth]byte // sized for full 320-wide row; ComposeScanline takes the centred 256 pixels
}

// SetTilemap attaches the tilemap layer (Layer 3). nil unhooks.
// Compositor uses the tilemap's "on_top" bit (NR$6B bit 0) to
// decide whether to paint it after every other layer or under
// ULA; for now we honour on_top by painting it last when set.
func (c *Compositor) SetTilemap(t *tilemap.Tilemap) { c.tilemap = t }

// HasActiveTilemap reports whether the tilemap layer is wired and
// enabled. The ULA's full-screen border pass uses this to decide
// whether to call ComposeBorderRow. An 80-column tilemap is excluded
// here: it is 640 pixels wide and rendered through the wide path
// (ComposeWideTilemapRow), not the 320-pixel inner/border passes.
func (c *Compositor) HasActiveTilemap() bool {
	return c.tilemap != nil && c.tilemap.Enabled() && c.pal != nil && !c.tilemap.Is80Col()
}

// TilemapIs80Col reports whether the active tilemap is in 80-column
// (640-pixel) mode. The ULA renders that through the wide path; the
// 320-pixel inner/border passes (ComposeScanline / ComposeBorderRow /
// HasActiveTilemap) deliberately skip the tilemap in that case.
func (c *Compositor) TilemapIs80Col() bool {
	return c.tilemap != nil && c.tilemap.Enabled() && c.pal != nil && c.tilemap.Is80Col()
}

// ComposeWideTilemapRow renders the 80-column tilemap row at its native
// 640-pixel width and composites it over dst — a 640-pixel RGBA row that
// already holds the horizontally-doubled lower layers (ULA + Layer 2 +
// sprites). A pixel is transparent when the low nibble of its palette
// index matches the tilemap transparency nibble, or (when on_top is off)
// when its low nibble is 0; otherwise it is opaque.
func (c *Compositor) ComposeWideTilemapRow(tilemapY int, dst []byte) {
	if !c.TilemapIs80Col() {
		return
	}
	tilemapPal := c.pal.PaletteForLayer(palette.LayerTilemap)
	if tilemapPal == nil {
		return
	}
	var scan [2 * FullWidth]byte // 640 pixels = 80 tiles × 8px
	c.tilemap.RenderScanline(tilemapY, scan[:])
	onTop := c.tilemap.OnTop()
	tmTransparentNibble := c.tilemapTrans
	n := len(dst) / 4
	if n > len(scan) {
		n = len(scan)
	}
	for x := 0; x < n; x++ {
		idx := scan[x]
		if idx&0x0F == tmTransparentNibble {
			continue
		}
		if !onTop && idx&0x0F == 0 {
			continue
		}
		r, g, b := tilemapPal.RGB(idx)
		off := x * 4
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
}

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

// ComposeWideLayer2Row overlays the Layer 2 hi-res row (320 or 640 px,
// NR$70 resolution 1/2) onto dst as RGBA at the layer's native width —
// used by the display path where Layer 2 spans the full Next display
// rather than the inner 256-wide rectangle. Transparent pixels (index ==
// the global transparency) leave dst untouched, so the caller pre-fills
// the background (border / ULA). dst must hold LineWidth()*4 bytes.
func (c *Compositor) ComposeWideLayer2Row(y int, dst []byte) {
	if c.l2 == nil || !c.l2.Enabled() || c.pal == nil {
		return
	}
	l2Pal := c.pal.PaletteForLayer(palette.LayerLayer2)
	if l2Pal == nil {
		return
	}
	w := c.l2.LineWidth()
	if w > 2*FullWidth || len(dst) < w*4 {
		return
	}
	var scan [2 * FullWidth]byte // up to 640 indices
	c.l2.RenderScanline(y, scan[:w])
	for x := 0; x < w; x++ {
		idx := scan[x]
		if c.l2Transparent(l2Pal, idx) {
			continue
		}
		r, g, b := l2Pal.RGB(idx)
		off := x * 4
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
}

// HiResLayer2Active reports whether Layer 2 is enabled in a hi-res mode
// (NR$70 resolution 1 or 2), so the display path drives the wide L2 pass.
func (c *Compositor) HiResLayer2Active() bool {
	return c.l2 != nil && c.l2.Enabled() && c.pal != nil && c.l2.Resolution() != 0
}

// Layer2Width returns the active Layer 2 framebuffer width (256/320/640).
func (c *Compositor) Layer2Width() int {
	if c.l2 == nil {
		return Width
	}
	return c.l2.LineWidth()
}

// ComposeBorderRow paints tilemap content over the border-area
// pixels of a full-screen row. Used by ULA.applyNextCompositor
// AFTER the inner 256-wide pass — covers the left 32-px and right
// 32-px border, plus the full 320-px width of border rows above
// and below the classic screen area.
//
// `dst` is 320×4 RGBA bytes for row `tilemapY` (relative to tilemap
// origin = top-left of the full 320×256 Next display).
// `isInBorderArea(x)` returns true for x values OUTSIDE the central
// 256-px inner area — those are the pixels the border pass paints;
// inner pixels are left alone (the inner pass already handled them).
//
// For rows above/below the classic 192-line screen, every x is in
// the border area; for screen rows, only x < 32 and x >= 32+256.
func (c *Compositor) ComposeBorderRow(tilemapY int, dst []byte, isInBorderArea func(x int) bool) {
	if !c.HasActiveTilemap() {
		return
	}
	tilemapPal := c.pal.PaletteForLayer(palette.LayerTilemap)
	if tilemapPal == nil {
		return
	}
	var scan [FullWidth]byte
	c.tilemap.RenderScanline(tilemapY, scan[:])
	// Same on_top rule as the inner pass: when NR$6B bit 0 is set
	// the FPGA paints palette[0] for nibble-0 pixels rather than
	// letting the ULA border colour show through. Without this the
	// outer 32-px border + 32-px top/bottom strips of the testcard
	// would show through to whatever ULA BorderColour boot.bin
	// happens to have left (e.g. blue) instead of the tilemap
	// background black, mismatching the inner screen and producing
	// a green/blue-banded "frame" effect.
	onTop := c.tilemap.OnTop()
	for x := 0; x < FullWidth; x++ {
		if !isInBorderArea(x) {
			continue
		}
		idx := scan[x]
		if idx == 0 && !onTop {
			continue // transparent — ULA border shows
		}
		r, g, b := tilemapPal.RGB(idx)
		off := x * 4
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
}

// HasActiveSprites reports whether the sprite layer is wired and enabled, so
// ULA.applyNextCompositor knows whether to run the sprite border pass.
func (c *Compositor) HasActiveSprites() bool {
	return c.sprites != nil && c.sprites.Enabled() && c.pal != nil
}

// ComposeSpriteBorderRow paints sprite pixels over the border-area pixels of a
// full-screen row, the sprite counterpart to ComposeBorderRow. Sprites are
// frame-relative (the 320x256 frame), so frameY is the sprite vcounter for this
// image row and dst is indexed directly by frame X (= image column). Used AFTER
// the inner paper pass to cover the top/bottom border strips (where games park
// HUD sprites, e.g. Nextoid's SHIPS/SCORE row at Y=224-225) and the left/right
// 32-px borders of screen rows. The sprite engine's own over-border clip
// (NR$15 bit 1) decides whether border sprites are visible at all.
func (c *Compositor) ComposeSpriteBorderRow(frameY int, dst []byte, isInBorderArea func(x int) bool) {
	if !c.HasActiveSprites() {
		return
	}
	spritePal := c.pal.PaletteForLayer(palette.LayerSprites)
	if spritePal == nil {
		return
	}
	var scan [FullWidth]byte
	c.sprites.RenderScanline(frameY, scan[:], FullWidth)
	for x := 0; x < FullWidth; x++ {
		if !isInBorderArea(x) {
			continue
		}
		idx := scan[x]
		if idx == 0 || idx == c.spriteTrans {
			continue // uncovered sentinel or transparency index
		}
		r, g, b := spritePal.RGB(idx)
		off := x * 4
		dst[off+0], dst[off+1], dst[off+2], dst[off+3] = r, g, b, 0xFF
	}
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
	c.recomputeULATrans()
}

// SetULAPalette installs the ULA's 16-colour palette so the compositor can
// resolve the ULA transparency colour (a transparent ULA pixel carries
// ulaPalette[NR$14]). Until this is called the ULA-transparency features
// (SUL stencil, NR$4A fallback) stay inert.
func (c *Compositor) SetULAPalette(pal [16]color.RGBA) {
	c.ulaPalette = pal
	c.ulaPaletteSet = true
	c.recomputeULATrans()
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

// recomputeULATrans precomputes the ULA transparency RGBA from NR$14 and
// the ULA palette. Inactive when the palette is unset or NR$14 >= 16 (no
// standard 16-colour ULA pixel can match), so the SUL stencil + NR$4A
// fallback are no-ops for the default $E3.
func (c *Compositor) recomputeULATrans() {
	c.ulaTransActive = false
	if !c.ulaPaletteSet || c.transparency >= 16 {
		return
	}
	p := c.ulaPalette[c.transparency]
	c.ulaTransActive = true
	c.ulaTransRGBA = [4]byte{p.R, p.G, p.B, p.A}
}

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
func (c *Compositor) ComposeScanline(y int, ulaRGBA []byte, dst []byte) {
	if len(dst) < Width*4 {
		return
	}
	// Determine which layers contribute this frame.
	// A hi-res Layer 2 (NR$70 resolution 1/2) is 320/640 px wide and is
	// composited through the wide display path (renderHiResLayer2 +
	// ComposeWideLayer2Row), so the inner 256-wide pass skips it here —
	// mirroring how an 80-column tilemap is skipped above.
	doL2 := c.l2 != nil && c.l2.Enabled() && c.pal != nil && c.l2.Resolution() == 0
	doSprites := c.sprites != nil && c.sprites.Enabled() && c.pal != nil
	// An 80-column tilemap is 640px wide and rendered through the wide
	// path, so the 320-pixel inner pass skips it here.
	doTilemap := c.tilemap != nil && c.tilemap.Enabled() && c.pal != nil && !c.tilemap.Is80Col()
	if !doL2 && !doSprites && !doTilemap {
		copy(dst[:Width*4], ulaRGBA[:Width*4])
		return
	}

	// Pre-fetch active layers' scanlines + their palettes.
	var l2Scanline []byte
	var l2Pal *palette.Palette
	if doL2 {
		// PaletteForLayer reads the per-layer "first/second"
		// selection (NextReg 0x43 bits 4-7) independently of the
		// write-target bits. Sprint 6's mistake was reading
		// Selected() (the write target) and assuming it meant
		// the active palette.
		l2Pal = c.pal.PaletteForLayer(palette.LayerLayer2)
		if l2Pal == nil {
			doL2 = false
		} else {
			l2Scanline = c.l2Scratch[:]
			c.l2.RenderScanline(y, l2Scanline)
		}
	}
	var tilemapScan []byte
	var tilemapPal *palette.Palette
	if doTilemap {
		// Per FPGA zxnext.vhd:6981 the tilemap pixel address has
		// bit 9 = 1, bit 8 = nr_6b bit 4 (tm_palette_select), bits
		// 7:0 = tile pixel. The FPGA palette SRAM is 4 logical
		// banks of 256 entries: address "00" = ULA First, "01" =
		// ULA Second, "10" = Tilemap First, "11" = Tilemap Second.
		// Our Zeus-aligned 8-palette enum keeps the same physical
		// SRAM banks at indices 0,1 (ULA) and 6,7 (Tilemap), so
		// the natural mapping is palettes[6 + activeTilemap].
		tilemapPal = c.pal.PaletteForLayer(palette.LayerTilemap)
		if tilemapPal == nil {
			doTilemap = false
		} else {
			// Render the FULL 320-pixel tilemap row. The inner
			// 256-wide compose pass uses the centred 256 pixels
			// (cols 32..287 of the tilemap = the cols that overlap
			// the classic-screen area when tilemap col 0 maps to
			// image col 0). See sram_pre_layer2_A21_A13 / FPGA
			// 320-pixel video framing — tilemap is anchored to the
			// full screen including the 32-px border, not to the
			// inner 256-wide rectangle.
			c.tilemap.RenderScanline(y, c.tilemapScratch[:])
			tilemapScan = c.tilemapScratch[BorderOffsetX : BorderOffsetX+Width]
		}
	}
	var spriteScan []byte
	var spritePal *palette.Palette
	if doSprites {
		spritePal = c.pal.PaletteForLayer(palette.LayerSprites)
		if spritePal == nil {
			doSprites = false
		} else {
			spriteScan = c.spriteScratch[:]
			// Zero the buffer; the sprite engine only OVERWRITES
			// pixels it covers, leaving previous content otherwise.
			for i := range spriteScan {
				spriteScan[i] = 0
			}
			// Sprites live in FRAME coordinates (320x256, paper at 32,32).
			// Compose the paper: paper row y is frame row y+SpriteFrameYTop,
			// rendered full-width so frame X (incl. the 32px paper offset) is
			// available; paintSprites reads [paperX+BorderOffsetX].
			c.sprites.RenderScanline(y+SpriteFrameYTop, spriteScan, FullWidth)
		}
	}

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
	// ulaTransparentAt reports whether the ULA pixel at off equals the
	// global transparency colour (active only when NR$14 < 16 + the ULA
	// palette is known — see recomputeULATrans). When inactive this is
	// always false, so paintBase/paintULAStencil collapse to paintULA and
	// the verified compositing is unchanged.
	ulaTransparentAt := func(off int) bool {
		return c.ulaTransActive &&
			ulaRGBA[off+0] == c.ulaTransRGBA[0] && ulaRGBA[off+1] == c.ulaTransRGBA[1] &&
			ulaRGBA[off+2] == c.ulaTransRGBA[2] && ulaRGBA[off+3] == c.ulaTransRGBA[3]
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
	paintL2 := func(off, x int) {
		if !doL2 {
			return
		}
		if idx := l2Scanline[x]; !c.l2Transparent(l2Pal, idx) {
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
		if !doL2 {
			return
		}
		idx := l2Scanline[x]
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
		if idx := spriteScan[x+BorderOffsetX]; idx != 0 && idx != c.spriteTrans {
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
		idx := tilemapScan[x]
		// FPGA transparency: the LOW nibble of the palette index is
		// compared to NR$4C(3:0). When equal -> transparent regardless
		// of on_top. Otherwise the pixel is opaque AND covers ULA
		// only when tm_on_top is set OR the tile flag asks for it.
		if (idx & 0x0F) == tmTransparentNibble {
			return
		}
		if !tmOnTop {
			// Strict reading of tilemap.vhd:388 requires the
			// per-pixel below bit; Sprint N approximates by saying
			// "transparent over ULA when on_top is off and nibble
			// is 0" — preserves the legacy compositing for non-
			// testcard content that relies on it.
			if (idx & 0x0F) == 0 {
				return
			}
		}
		r, g, b := tilemapPal.RGB(idx)
		dst[off+0] = r
		dst[off+1] = g
		dst[off+2] = b
		dst[off+3] = 0xFF
	}
	for x := 0; x < Width; x++ {
		off := x * 4
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
		}
	}
}
