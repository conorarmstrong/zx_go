package main

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/compositor"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The compositor keeps a row's Layer 2, tilemap and sprite renders across the
// Copper writes that land on that row, and drops them only when a write names
// a register the renders read (compositor.LayerRenderReads). That set was
// enumerated by reading the layers; this test is what stops it being wrong.
//
// For every NextReg the Copper can name ($01..$7F), it builds two identical
// machines with all three layers on and drawing, runs a Copper list that
// writes that register several times across one paper row, and renders the
// frame once with the cache and once without. A register the renders read but
// the set leaves out shows as a difference on that row.
func TestLayerCacheMatchesUncachedForEveryCopperRegister(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 254 Next machines")
	}
	for reg := 1; reg < 0x80; reg++ {
		cached := layerCacheFrame(t, byte(reg), true)
		fresh := layerCacheFrame(t, byte(reg), false)
		if cached == nil || fresh == nil {
			return
		}
		if !bytes.Equal(cached, fresh) {
			t.Errorf("NR$%02X: a Copper write changes the layer renders but does not drop the cache", reg)
		}
	}
}

// TestLayerCacheGuardCatchesEachRegisterLeftOut is the mutation check on the
// test above: leave any one register out of the invalidation set and the frame
// must change. A register whose omission goes unseen is either not read by any
// render, and should leave the set, or not exercised by the scene, and the
// scene needs widening. NR$02 and NR$03 are in the set as a precaution and are
// exempt.
func TestLayerCacheGuardCatchesEachRegisterLeftOut(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 54 Next machines")
	}
	for reg := 1; reg < 0x80; reg++ {
		r := byte(reg)
		if !compositor.LayerRenderReads(r) || r == 0x02 || r == 0x03 {
			continue
		}
		fresh := layerCacheFrameWith(t, r, false, 0)
		mutant := layerCacheFrameWith(t, r, true, r)
		if fresh == nil || mutant == nil {
			return
		}
		if bytes.Equal(fresh, mutant) {
			t.Errorf("NR$%02X: leaving it out of the invalidation set changes nothing", r)
		}
	}
}

// leaveOut is a compositor whose invalidation set lacks one register.
type leaveOut struct {
	*compositor.Compositor
	reg byte
}

func (l leaveOut) LayerRenderReads(reg byte) bool {
	return reg != l.reg && compositor.LayerRenderReads(reg)
}

// layerCacheRow is the paper row the Copper writes land on.
const layerCacheRow = 40

// layerCacheFrame builds a Next with Layer 2, the tilemap and 16 sprites all
// covering paper row layerCacheRow, loads a Copper list that writes reg six
// times on each of four rows near it, renders one frame and returns its
// pixels.
func layerCacheFrame(t *testing.T, reg byte, cache bool) []byte {
	t.Helper()
	return layerCacheFrameWith(t, reg, cache, 0)
}

// layerCacheFrameWith is layerCacheFrame with register omit left out of the
// cache's invalidation set; 0 leaves the set whole.
func layerCacheFrameWith(t *testing.T, reg byte, cache bool, omit byte) []byte {
	t.Helper()
	emu, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Skipf("Next core unavailable: %v", err)
		return nil
	}
	if emu.nextRegs == nil || emu.nextCopper == nil || emu.nextSprites == nil {
		t.Skip("Next layers not wired on this build")
		return nil
	}
	d := emu.nextRegs
	// The palette RAM has no initial contents; the firmware loads it.
	emu.nextPalette.LoadFirmwareDefaults()
	for _, sel := range []byte{palette.PaletteTilemapFirst, palette.PaletteTilemapSecond} {
		p := emu.nextPalette.Palette(int(sel))
		for i := 0; i < 256; i++ {
			p.Set(byte(i), uint16(255-i)<<1)
		}
	}

	// Layer 2 at 256x192 from bank 8, every byte different along a row.
	for b := 8; b < 20; b++ {
		p := emu.mem.GetPage(b)
		for i := range p {
			p[i] = byte(i*7 + b*13 + i/256*3)
			if i/8%3 == 0 {
				p[i] = 0xE3 // transparent, so the tilemap shows through
			}
		}
	}
	d.WriteReg(0x12, 8)
	d.WriteReg(0x69, 0x80)

	// The tilemap: 40x32 with attributes, map at bank 5 $0000, tile
	// definitions at bank 5 $1400.
	p5 := emu.mem.GetPage(5)
	for k := 0; k < 0x1400/2; k++ {
		p5[2*k] = byte(k * 7)      // tile
		p5[2*k+1] = byte(k * 0x13) // attribute: bit 0 (ULA over) alternates
	}
	for i := 0x1400; i < 0x4000; i++ {
		p5[i] = byte(i*11 + i/32)
	}
	d.WriteReg(0x6E, 0x00)
	d.WriteReg(0x6F, 0x14)
	d.WriteReg(0x6B, 0x80)
	d.WriteReg(0x4C, 0x00)

	// Sprites: visible, over the border, sixteen of them across the rows,
	// extended so the fifth attribute byte counts, and doubled in height.
	d.WriteReg(0x15, 0x03)
	emu.nextSprites.SetPatternAddr(0)
	for a := 0; a < sprite.PatternRAMSize; a++ {
		emu.nextSprites.WritePatternByte(byte(a*3 + a/16))
	}
	for i := 0; i < 16; i++ {
		emu.nextSprites.Set(i, sprite.Attr{
			X:        int16(32 + i*18),
			Y:        int16(32 + layerCacheRow - 4 - i%4),
			Pattern:  byte(i),
			Palette:  byte(i),
			Visible:  true,
			Extended: true,
			Byte4:    0x02, // Y scale 2: 32 lines tall
		})
	}
	// Attribute writes go to sprite 8, right of every write column, so a
	// change to it shows on the part of the row composed after the write.
	d.WriteReg(0x34, 8)

	// The Copper list writes reg six times on each of five rows. The first
	// set of values keeps bits 7 and 0 set, so writes to an enable register
	// leave its layer on and exercise the other bits. Before the last two
	// rows but one it reconfigures: tilemap attributes stripped (NR$6C counts),
	// Layer 2 at 320x256 (NR$71 counts), sprite border clip on (NR$19 counts).
	emu.ula.SetNextLayerCache(cache)
	if omit != 0 {
		emu.ula.SetNextCompositor(leaveOut{emu.ula.NextCompositor().(*compositor.Compositor), omit})
	}
	c := emu.nextCopper
	c.SetWritePtrLow(0)
	c.SetWritePtrHighAndMode(0x00)
	put := func(w uint16) {
		c.WriteData(byte(w >> 8))
		c.WriteData(byte(w))
	}
	wait := func(x, y int) uint16 { return 0x8000 | uint16(x&0x3F)<<9 | uint16(y&0x1FF) }
	move := func(r, v byte) uint16 { return uint16(r&0x7F)<<8 | uint16(v) }
	keepOn := []byte{0x81, 0xFF, 0xA5, 0xC3, 0xE7, 0x99}
	mixed := []byte{0x00, 0xFF, 0x5A, 0xA5, 0x01, 0x80}
	writes := func(row int, vals []byte) {
		for i, v := range vals {
			put(wait(1+i*3, row))
			put(move(reg, v))
		}
	}
	writes(layerCacheRow, keepOn)
	writes(layerCacheRow+2, mixed)
	put(wait(0, layerCacheRow+3))
	put(move(0x6B, 0xA0))
	put(move(0x70, 0x10))
	put(move(0x15, 0x23))
	writes(layerCacheRow+4, keepOn)
	writes(layerCacheRow+6, mixed)
	// A row that starts with reg cleared, so a layer disabled when the row is
	// first composed can be enabled partway along it.
	put(wait(31, layerCacheRow+7))
	put(move(reg, 0x00))
	writes(layerCacheRow+8, keepOn)
	put(wait(63, 0x1FF)) // park for the rest of the frame
	d.WriteReg(0x62, 0x40)

	img := emu.ula.Render()
	if img == nil {
		t.Fatalf("NR$%02X: Render returned no frame", reg)
	}
	out := make([]byte, len(img.Pix))
	copy(out, img.Pix)
	return out
}

// A saturated Copper list, a palette write every two copper clocks with no
// WAIT, rendered with and without the layer cache. This is the case ROADMAP
// item 3 measured at more than twice the frame budget.
//
//	go test ./cmd/zx_go -run XXX -bench SaturatedCopper
func BenchmarkSaturatedCopperFrame(b *testing.B) {
	for _, cache := range []bool{false, true} {
		name := "uncached"
		if cache {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			emu, err := newEmulator(roms.ModelNext)
			if err != nil {
				b.Skipf("Next core unavailable: %v", err)
			}
			d := emu.nextRegs
			emu.nextPalette.LoadFirmwareDefaults()
			d.WriteReg(0x69, 0x80)
			d.WriteReg(0x15, 0x01)
			for i := 0; i < 16; i++ {
				emu.nextSprites.Set(i, sprite.Attr{X: int16(32 + i*18), Y: int16(40 + i*10), Visible: true})
			}
			emu.ula.SetNextLayerCache(cache)
			c := emu.nextCopper
			c.SetWritePtrLow(0)
			c.SetWritePtrHighAndMode(0x00)
			for i := 0; i < 1024; i++ {
				c.WriteData(0x41)
				c.WriteData(byte(i))
			}
			d.WriteReg(0x62, 0xC0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				emu.ula.Render()
			}
		})
	}
}
