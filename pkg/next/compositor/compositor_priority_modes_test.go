package compositor

import (
	"image/color"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// NextReg 0x15 bits 4:2 select one of EIGHT layer orderings, not four
// (zxnext.vhd:5230-5234; the full resolver lives in Mix(), verified against
// the captured FPGA golden by TestMixerMatchesFPGAGolden):
//
//	000 S L U   001 L S U   010 S U L   011 L U S
//	100 U S L   101 U L S   110 blend   111 blend-5
//
// The live compositor only implemented 000-011. Modes 100-111 fell through
// the switch with no case at all, so Layer 2 AND the sprite layer vanished
// entirely and the frame showed bare ULA. These tests pin the four that were
// missing.

// rgb3 packs three 3-bit channels into the 9-bit palette word.
func rgb3(r, g, b uint16) uint16 { return r<<6 | g<<3 | b }

// modeFixture builds a compositor with one opaque Layer 2 colour, one opaque
// sprite colour, and a known ULA transparency colour.
type modeFixture struct {
	c       *Compositor
	ulaRGBA []byte
	dst     []byte
}

const (
	// The transparency index (NR$14 / NR$4B). Mapped to itself in every
	// palette so the mapped colour resolves back to the same index.
	fxTrans = 7
	// Layer 2 colour: 3-bit (5,1,0).
	fxL2R, fxL2G, fxL2B = 5, 1, 0
	// Sprite colour: 3-bit (0,7,0) — green, distinguishable from L2.
	fxSpR, fxSpG, fxSpB = 0, 7, 0
	// The ULA transparency colour: 3-bit (7,0,7), magenta.
	fxTransR, fxTransG, fxTransB = 7, 0, 7
	// The opaque ULA colour: 3-bit (4,2,0). Kept 3-bit-clean so the blend
	// modes can state their expectations in hardware channel terms.
	fxULAR, fxULAG, fxULAB = 4, 2, 0
)

func newModeFixture(t *testing.T, mode PriorityMode, withSprite bool) *modeFixture {
	t.Helper()

	pal := palette.NewBank()

	pal.Select(palette.PaletteLayer2First)
	pal.Active().Set(5, rgb3(fxL2R, fxL2G, fxL2B))
	pal.Active().Set(fxTrans, uint16(fxTrans)<<1) // resolves to NR$14 -> transparent
	pal.Select(palette.PaletteSpritesFirst)
	pal.Active().Set(1, rgb3(fxSpR, fxSpG, fxSpB))
	pal.Select(0)

	// Layer 2 row 0: pixel 0 = index 5 (opaque), pixel 1 = index 7 (transparent).
	bank := make([]byte, 16384)
	bank[0] = 5
	bank[1] = fxTrans
	l2 := layer2.New(&fakeBanks{banks: map[int][]byte{0: bank}})
	l2.SetActiveBank(0)
	l2.SetEnabled(true)

	c := New(pal, l2)
	c.SetPrioritySource(fixedPriority{mode})

	if withSprite {
		sp := sprite.New()
		sp.SetEnabled(true)
		for i := 0; i < 128; i++ {
			sp.SetPatternAddr(uint16(i))
			sp.WritePatternByte(0x01) // 8bpp: byte == palette index 1
		}
		sp.Set(0, sprite.Attr{X: 32, Y: 32, Pattern: 0, Visible: true})
		c.SetSprites(sp)
	}

	var ulaPal [16]color.RGBA
	ulaPal[fxTrans] = color.RGBA{
		R: expand3(fxTransR), G: expand3(fxTransG), B: expand3(fxTransB), A: 0xFF,
	}
	c.SetULAPalette(ulaPal)
	c.SetTransparency(fxTrans)
	c.SetFallbackColour(0x99, 0x88, 0x77)

	return &modeFixture{c: c, ulaRGBA: make([]byte, Width*4), dst: make([]byte, Width*4)}
}

// setULA fills the ULA scanline with an opaque colour, or with the
// transparency colour when transparent is true.
func (f *modeFixture) setULA(transparent bool) {
	r, g, b := expand3(fxULAR), expand3(fxULAG), expand3(fxULAB)
	if transparent {
		r, g, b = expand3(fxTransR), expand3(fxTransG), expand3(fxTransB)
	}
	for i := 0; i < Width; i++ {
		f.ulaRGBA[i*4+0], f.ulaRGBA[i*4+1] = r, g
		f.ulaRGBA[i*4+2], f.ulaRGBA[i*4+3] = b, 0xFF
	}
}

func (f *modeFixture) pixel(x int) [3]byte {
	f.c.ComposeScanline(0, f.ulaRGBA, f.dst)
	return [3]byte{f.dst[x*4+0], f.dst[x*4+1], f.dst[x*4+2]}
}

var (
	wantL2     = [3]byte{expand3(fxL2R), expand3(fxL2G), expand3(fxL2B)}
	wantSprite = [3]byte{expand3(fxSpR), expand3(fxSpG), expand3(fxSpB)}
	wantULAOp  = [3]byte{expand3(fxULAR), expand3(fxULAG), expand3(fxULAB)}
)

// TestComposeScanlineModeUSL pins NR$15 = 100: ULA over Sprites over Layer 2
// (Mix() case 4). Before the fix this mode dropped both Layer 2 and the
// sprite layer.
func TestComposeScanlineModeUSL(t *testing.T) {
	t.Run("opaque ULA wins over both", func(t *testing.T) {
		f := newModeFixture(t, ModeUSL, true)
		f.setULA(false)
		if got := f.pixel(0); got != wantULAOp {
			t.Errorf("pixel 0 = %v, want ULA %v", got, wantULAOp)
		}
	})
	t.Run("transparent ULA falls to the sprite", func(t *testing.T) {
		f := newModeFixture(t, ModeUSL, true)
		f.setULA(true)
		if got := f.pixel(0); got != wantSprite {
			t.Errorf("pixel 0 = %v, want sprite %v", got, wantSprite)
		}
	})
	t.Run("transparent ULA and no sprite falls to Layer 2", func(t *testing.T) {
		f := newModeFixture(t, ModeUSL, false)
		f.setULA(true)
		if got := f.pixel(0); got != wantL2 {
			t.Errorf("pixel 0 = %v, want Layer 2 %v", got, wantL2)
		}
	})
}

// TestComposeScanlineModeULS pins NR$15 = 101: ULA over Layer 2 over Sprites
// (Mix() case 5). The sprite and Layer 2 order is the opposite of USL, which
// is the only thing that distinguishes the two.
func TestComposeScanlineModeULS(t *testing.T) {
	t.Run("opaque ULA wins over both", func(t *testing.T) {
		f := newModeFixture(t, ModeULS, true)
		f.setULA(false)
		if got := f.pixel(0); got != wantULAOp {
			t.Errorf("pixel 0 = %v, want ULA %v", got, wantULAOp)
		}
	})
	t.Run("transparent ULA falls to Layer 2, not the sprite", func(t *testing.T) {
		f := newModeFixture(t, ModeULS, true)
		f.setULA(true)
		if got := f.pixel(0); got != wantL2 {
			t.Errorf("pixel 0 = %v, want Layer 2 %v", got, wantL2)
		}
	})
	t.Run("transparent ULA and transparent Layer 2 falls to the sprite", func(t *testing.T) {
		f := newModeFixture(t, ModeULS, true)
		f.setULA(true)
		// Layer 2 pixel 1 is the transparency index.
		if got := f.pixel(1); got != wantSprite {
			t.Errorf("pixel 1 = %v, want sprite %v", got, wantSprite)
		}
	})
}

// TestComposeScanlineModeBlendAdds pins NR$15 = 110, the additive blend:
// Layer 2 is summed with the ULA per 3-bit channel and clamped at 7
// (Mix() case 6). With no tilemap and NR$68 at its reset value, an opaque
// Layer 2 pixel resolves to the blend.
func TestComposeScanlineModeBlendAdds(t *testing.T) {
	f := newModeFixture(t, ModeBlend, false)
	f.setULA(false) // the ULA colour still feeds the blend arithmetic

	// r: 5 + 4 = 9 -> clamped to 7. g: 1 + 2 = 3. b: 0 + 0 = 0.
	want := [3]byte{expand3(7), expand3(3), expand3(0)}
	if got := f.pixel(0); got != want {
		t.Errorf("blend pixel = %v, want %v", got, want)
	}
}

// TestComposeScanlineModeBlendMinus5 pins NR$15 = 111: the same sum with the
// hardware's minus-5 curve applied (Mix() case 7) — values <= 4 floor to 0,
// values >= 12 saturate to 7, the rest lose 5.
func TestComposeScanlineModeBlendMinus5(t *testing.T) {
	f := newModeFixture(t, ModeBlend5, false)
	f.setULA(false)

	// r: 5 + 4 = 9 -> 9 - 5 = 4. g: 1 + 2 = 3 -> <= 4 so 0. b: 0 -> 0.
	want := [3]byte{expand3(4), expand3(0), expand3(0)}
	if got := f.pixel(0); got != want {
		t.Errorf("blend-5 pixel = %v, want %v", got, want)
	}
}

// TestPriorityModeConstantsCoverAllEight guards the decode surface: NR$15
// bits 4:2 are a 3-bit field, so every value 0-7 must name a mode.
func TestPriorityModeConstantsCoverAllEight(t *testing.T) {
	names := map[PriorityMode]string{
		ModeSLU: "SLU", ModeLSU: "LSU", ModeSUL: "SUL", ModeLUS: "LUS",
		ModeUSL: "USL", ModeULS: "ULS", ModeBlend: "blend", ModeBlend5: "blend-5",
	}
	for v := 0; v < 8; v++ {
		if _, ok := names[PriorityMode(v)]; !ok {
			t.Errorf("NR$15 priority %d has no named mode", v)
		}
	}
}
