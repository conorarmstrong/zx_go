package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// overBorderMock is a NextCompositor with active sprites that records which
// frame-Y rows the display path asks it to paint and drops a marker pixel in
// the bottom over-border strip (frame Y 250).
type overBorderMock struct{ rows map[int]bool }

func (m *overBorderMock) ComposeScanline(y int, ula, dst []byte)               {}
func (m *overBorderMock) HasActiveTilemap() bool                               { return false }
func (m *overBorderMock) ComposeBorderRow(y int, dst []byte, f func(int) bool) {}
func (m *overBorderMock) HasActiveSprites() bool                               { return true }
func (m *overBorderMock) ComposeSpriteBorderRow(y int, dst []byte, f func(int) bool) {
	if m.rows == nil {
		m.rows = map[int]bool{}
	}
	m.rows[y] = true
	if y == 250 { // bottom over-border strip — paint a marker at x=0
		dst[0], dst[1], dst[2], dst[3] = 0x12, 0x34, 0x56, 0xFF
	}
}
func (m *overBorderMock) TilemapIs80Col() bool                    { return false }
func (m *overBorderMock) ComposeWideTilemapRow(y int, dst []byte) {}
func (m *overBorderMock) HiResLayer2Active() bool                 { return false }
func (m *overBorderMock) Layer2Width() int                        { return 320 }
func (m *overBorderMock) ComposeWideLayer2Row(y int, dst []byte)  {}

// TestNextRenderShowsOverBorderSprites locks in that, when the Next sprite
// layer is active, the display returns the full 320x256 over-border frame (not
// the cropped 320x240) and runs the sprite border pass over the top (frame Y
// 0..7) and bottom (frame Y 248..255) strips the classic 24-px border crops.
// This is the NextBASIC-Invaders fix: NBI parks the player ship at sprite
// Y=240 (in the bottom over-border band), which the 240-line frame cut off.
func TestNextRenderShowsOverBorderSprites(t *testing.T) {
	testDir := "test_roms_overborder"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)
	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	u := New(mem, keyboard.New())
	m := &overBorderMock{}
	u.SetNextCompositor(m)

	img := u.Render()

	if h := img.Bounds().Dy(); h != 256 {
		t.Fatalf("Next frame height = %d, want 256 (full over-border sprite frame)", h)
	}
	// The marker painted at frame Y 250 (bottom over-border strip) must survive
	// into the returned frame — i.e. the ship band is actually composited.
	off := 250 * img.Stride
	if p := img.Pix[off : off+4]; p[0] != 0x12 || p[1] != 0x34 || p[2] != 0x56 {
		t.Errorf("over-border sprite at frame Y 250 missing from frame: got % X", img.Pix[off:off+4])
	}
	// Both cropped strips must have been offered to the sprite pass.
	if !m.rows[0] || !m.rows[255] {
		t.Errorf("over-border strips not painted: sawY0=%v sawY255=%v", m.rows[0], m.rows[255])
	}
}

// TestNextRenderNoSpritesStaysClassicHeight verifies the frame stays 320x240
// when the Next sprite layer is inactive (no over-border band needed), so the
// taller frame is scoped to sprite-using content only.
func TestNextRenderNoSpritesStaysClassicHeight(t *testing.T) {
	testDir := "test_roms_overborder_off"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)
	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	u := New(mem, keyboard.New())
	u.SetNextCompositor(&noSpriteMock{})

	img := u.Render()
	if h := img.Bounds().Dy(); h != TotalHeight {
		t.Errorf("no-sprite Next frame height = %d, want %d", h, TotalHeight)
	}
}

type noSpriteMock struct{}

func (m *noSpriteMock) ComposeScanline(y int, ula, dst []byte)                     {}
func (m *noSpriteMock) HasActiveTilemap() bool                                     { return false }
func (m *noSpriteMock) ComposeBorderRow(y int, dst []byte, f func(int) bool)       {}
func (m *noSpriteMock) HasActiveSprites() bool                                     { return false }
func (m *noSpriteMock) ComposeSpriteBorderRow(y int, dst []byte, f func(int) bool) {}
func (m *noSpriteMock) TilemapIs80Col() bool                                       { return false }
func (m *noSpriteMock) ComposeWideTilemapRow(y int, dst []byte)                    {}
func (m *noSpriteMock) HiResLayer2Active() bool                                    { return false }
func (m *noSpriteMock) Layer2Width() int                                           { return 320 }
func (m *noSpriteMock) ComposeWideLayer2Row(y int, dst []byte)                     {}
