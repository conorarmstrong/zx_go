package main

import (
	"fmt"
	"image"
	"image/color"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// nextDebugProvider feeds the debugger's Next-state panel from an
// already-constructed ModelNext emulator. Pulls MMU slot map from
// pkg/memory, divMMC pager state from the ULA's wired pager, and
// NextReg values from the dispatcher.
type nextDebugProvider struct {
	emu *emulator
}

func newNextDebugProvider(e *emulator) debugger.NextProvider {
	return &nextDebugProvider{emu: e}
}

func (p *nextDebugProvider) MMUSlots() [8]int {
	var out [8]int
	for i := 0; i < 8; i++ {
		out[i] = int(p.emu.mem.GetMMU(byte(i)))
	}
	return out
}

func (p *nextDebugProvider) DivMMCState() string {
	pager, ok := p.emu.ula.NextDivMMC().(*divmmc.Pager)
	if !ok || pager == nil {
		return "not wired"
	}
	return fmt.Sprintf("paged=%v mapram=%v automap=%v",
		pager.IsPagedIn(), pager.MAPRAM(), pager.AutomapEnabled())
}

func (p *nextDebugProvider) NextRegs() map[uint8]byte {
	out := make(map[uint8]byte, len(debugger.NextRegsOfInterest))
	if p.emu.nextRegs == nil {
		return out
	}
	for _, r := range debugger.NextRegsOfInterest {
		out[r] = p.emu.nextRegs.ReadReg(r)
	}
	return out
}

// PaletteRGBA implements debugger.PaletteRGBAProvider: the active
// 256-entry palette as expanded 8-bit RGBA, for the graphical Palette
// tab. All-black when no palette bank is wired.
func (p *nextDebugProvider) PaletteRGBA() [256]color.RGBA {
	var out [256]color.RGBA
	if p.emu.nextPalette == nil {
		return out
	}
	pal := p.emu.nextPalette.Active()
	if pal == nil {
		return out
	}
	for i := 0; i < 256; i++ {
		r, g, b := pal.RGB(byte(i))
		out[i] = color.RGBA{R: r, G: g, B: b, A: 255}
	}
	return out
}

// VisibleSprites implements debugger.SpriteVizProvider: each visible
// sprite's 16×16 4bpp pattern resolved through the sprite palette,
// for the graphical Sprite viewer. Pattern layout matches the sprite
// engine's RenderScanline (Pattern*128 base, 8 bytes/row, high nibble
// = left pixel, index 0 = transparent).
func (p *nextDebugProvider) VisibleSprites() []debugger.SpriteSnapshot {
	eng := p.emu.nextSprites
	if eng == nil || !eng.Enabled() {
		return nil
	}
	var pal *palette.Palette
	if p.emu.nextPalette != nil {
		pal = p.emu.nextPalette.PaletteForLayer(palette.LayerSprites)
	}
	colorOf := func(idx byte) color.RGBA {
		if pal == nil {
			return color.RGBA{}
		}
		r, g, b := pal.RGB(idx)
		return color.RGBA{R: r, G: g, B: b, A: 255}
	}

	var out []debugger.SpriteSnapshot
	for i := 0; i < sprite.MaxSprites; i++ {
		a := eng.Sprite(i)
		if a == nil || !a.Visible {
			continue
		}
		snap := debugger.SpriteSnapshot{Index: i}
		base := uint16(a.Pattern) * 128
		for row := 0; row < debugger.SpriteDim; row++ {
			for col := 0; col < debugger.SpriteDim; col++ {
				pix := eng.PatternByte(base + uint16(row*8+(col>>1)))
				var idx byte
				if col&1 == 0 {
					idx = pix >> 4
				} else {
					idx = pix & 0x0F
				}
				var c color.RGBA
				if idx != 0 { // 0 = transparent
					c = colorOf((idx + a.Palette) & 0xFF)
				}
				snap.Pixels[row*debugger.SpriteDim+col] = c
			}
		}
		out = append(out, snap)
	}
	return out
}

// layerColorOf returns an RGBA lookup over the given palette layer.
func (p *nextDebugProvider) layerColorOf(layer palette.Layer) func(byte) color.RGBA {
	var pal *palette.Palette
	if p.emu.nextPalette != nil {
		pal = p.emu.nextPalette.PaletteForLayer(layer)
	}
	return func(idx byte) color.RGBA {
		if pal == nil {
			return color.RGBA{}
		}
		r, g, b := pal.RGB(idx)
		return color.RGBA{R: r, G: g, B: b, A: 255}
	}
}

// Layer2Frame implements debugger.Layer2FrameProvider: the live
// Layer-2 framebuffer rendered through the Layer-2 palette.
func (p *nextDebugProvider) Layer2Frame() (image.Image, string) {
	l2 := p.emu.nextLayer2
	if l2 == nil {
		return nil, "unavailable"
	}
	rows := make([][]byte, layer2.Height)
	buf := make([]byte, layer2.Width)
	for y := 0; y < layer2.Height; y++ {
		l2.RenderScanline(y, buf)
		row := make([]byte, layer2.Width)
		copy(row, buf)
		rows[y] = row
	}
	img := debugger.RenderIndexed(rows, p.layerColorOf(palette.LayerLayer2))
	st := fmt.Sprintf("%dx%d", layer2.Width, layer2.Height)
	if !l2.Enabled() {
		st += "  (layer disabled)"
	}
	return img, st
}

// TilemapFrame implements debugger.TilemapFrameProvider: the live
// tilemap layer rendered through the tilemap palette.
func (p *nextDebugProvider) TilemapFrame() (image.Image, string) {
	tm := p.emu.nextTilemap
	if tm == nil {
		return nil, "unavailable"
	}
	rows := make([][]byte, tilemap.Height)
	buf := make([]byte, tilemap.Width40)
	for y := 0; y < tilemap.Height; y++ {
		tm.RenderScanline(y, buf)
		row := make([]byte, tilemap.Width40)
		copy(row, buf)
		rows[y] = row
	}
	img := debugger.RenderIndexed(rows, p.layerColorOf(palette.LayerTilemap))
	st := fmt.Sprintf("%dx%d", tilemap.Width40, tilemap.Height)
	if !tm.Enabled() {
		st += "  (layer disabled)"
	}
	return img, st
}
