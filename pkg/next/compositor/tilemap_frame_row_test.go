package compositor

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// The tilemap is addressed by the wide vertical counter, wvc (zxnext.vhd:4390,
// tilemap.vhd:326), which is 0 thirty-two lines above the paper
// (zxula_timing.vhd:477-499). So paper row 0 shows tilemap row 32, not row 0.
// Reading it as row 0 drew Sonic's level 32 pixels low, under a Sonic the game
// had placed on the ground.
func TestInnerPassReadsTheTilemapAtTheFrameRow(t *testing.T) {
	br := &fakeBankReader{}
	bank := make([]byte, 16384)
	// Map row 4 (frame rows 32..39): every cell is tile 1, foreground attr.
	for c := 0; c < 40; c++ {
		bank[4*80+2*c] = 0x01
		bank[4*80+2*c+1] = 0x0E // textmode fg index $0F
	}
	for r := 0; r < 8; r++ {
		bank[0x2000+8+r] = 0xFF // tile 1: every pixel foreground
	}
	br.banks[5] = bank
	tm := tilemap.New(br)
	tm.SetEnabled(true)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x20)
	tm.SetControl(0x08) // 40-col textmode

	pal := palette.NewBank()
	pal.Select(palette.PaletteTilemapFirst)
	pal.Active().Set(0x0F, 0b1_1100_0000) // red
	pal.Select(0)
	c := New(pal, nil)
	c.SetTilemap(tm)
	c.SetTilemapTransparency(0x00) // map row 0's index-0 background is see-through

	const sentinel = 0x11
	ula := make([]byte, Width*4)
	for i := range ula {
		ula[i] = sentinel
	}
	dst := make([]byte, Width*4)
	c.ComposeScanline(0, ula, dst)
	if dst[0] == sentinel {
		t.Error("paper row 0 did not show tilemap row 32 (frame row = paper row + 32)")
	}
}
