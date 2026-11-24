package compositor

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// NR$68 bit 7 clear disables the ULA's output, and the mixer then treats every
// ULA pixel as transparent: zxnext.vhd:7103,
//
//	ula_transparent <= '1' when (ula_mix_transparent = '1') or (ula_en_2 = '0')
//
// so a layer ordered beneath the ULA shows through it. Painting the disabled
// ULA as an opaque fallback-coloured layer hid the sprites of any title that
// disabled it in an order with the ULA above the sprites (TX-1696 in LUS).
func TestADisabledULAIsTransparent(t *testing.T) {
	pal := palette.NewBank()
	pal.Select(palette.PaletteSpritesFirst)
	pal.Active().Set(1, 0b1_1100_0000) // sprite = red
	pal.Select(0)
	sp := sprite.New()
	sp.SetEnabled(true)
	for i := 0; i < 256; i++ {
		sp.SetPatternAddr(uint16(i))
		sp.WritePatternByte(0x01)
	}
	sp.Set(0, sprite.Attr{X: 32, Y: 32, Pattern: 0, Visible: true})
	c := New(pal, nil)
	c.SetSprites(sp)
	c.SetPrioritySource(fakedPrio{ModeLUS}) // the ULA above the sprites
	c.SetFallbackColour(0, 0, 0)

	ula := make([]byte, Width*4)
	for i := 0; i < Width; i++ {
		ula[i*4], ula[i*4+1], ula[i*4+2], ula[i*4+3] = 0, 0, 0, 0xFF // opaque black
	}
	dst := make([]byte, Width*4)

	c.ComposeScanline(0, ula, dst)
	if dst[0] != 0 {
		t.Fatalf("enabled ULA: pixel 0 red=%d, want the opaque ULA over the sprite", dst[0])
	}
	c.SetULAEnabled(false)
	c.ComposeScanline(0, ula, dst)
	if dst[0] == 0 {
		t.Error("disabled ULA: the sprite under it did not show; a disabled ULA is transparent")
	}
	if dst[20*4] != 0 {
		t.Errorf("disabled ULA, no sprite: pixel 20 red=%d, want the fallback", dst[20*4])
	}
}

// A tilemap pixel flagged to sit below the ULA still shows where the ULA
// pixel is transparent (zxnext.vhd:7116: tm_rgb when tm_transparent = '0' and
// (tm_pixel_below_2 = '0' or ula_transparent = '1')).
func TestABelowTilemapPixelShowsThroughATransparentULA(t *testing.T) {
	br := &fakeBankReader{}
	bank := make([]byte, 16384)
	for cell := 0; cell < 40; cell++ {
		bank[cell*2] = 1      // tile 1
		bank[cell*2+1] = 0x01 // attribute bit 0: the ULA draws over this tile
	}
	for i := 0; i < 32; i++ {
		bank[0x20+i] = 0x55 // tile 1: every nibble index 5
	}
	br.banks[5] = bank
	tm := tilemap.New(br)
	tm.SetEnabled(true)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetControl(0x00)

	pal := palette.NewBank()
	pal.Select(palette.PaletteTilemapFirst)
	pal.Active().Set(5, 0b1_1100_0000) // red
	pal.Select(0)
	c := New(pal, nil)
	c.SetTilemap(tm)

	full := make([]byte, FullWidth*4)
	ulaRow := make([]byte, FullWidth*4)
	for i := 0; i < FullWidth; i++ { // RGB332 $E3, the default transparency
		ulaRow[i*4], ulaRow[i*4+1], ulaRow[i*4+2], ulaRow[i*4+3] = 0xE0, 0x00, 0xC0, 0xFF
	}
	c.ComposeFrameRowRange(0, ulaRow, full, 1, 0, FullWidth)
	if full[0] != 0xFF || full[1] != 0 {
		t.Errorf("pixel 0 = %d/%d/%d, want the tilemap's red through the transparent ULA", full[0], full[1], full[2])
	}
}
