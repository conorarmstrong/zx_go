package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The tilemap and the hi-res Layer 2 are addressed by the wide vertical
// counter, 0 at the top of the 256-line frame (zxula_timing.vhd:477-499). The
// 240-line image starts 8 lines into that frame, as the sprite border pass
// already allows for, so image row r is frame row r+8 for these layers too.

type frameRowMock struct {
	tilemap, wideTM, hiResL2 bool
	border, wide, l2         []int
}

func (m *frameRowMock) ComposeScanline(int, []byte, []byte)                {}
func (m *frameRowMock) ComposeScanlineRange(int, []byte, []byte, int, int) {}
func (m *frameRowMock) HasActiveTilemap() bool                             { return m.tilemap }
func (m *frameRowMock) ComposeBorderRow(y int, _ []byte, _ func(int) bool) {
	m.border = append(m.border, y)
}
func (m *frameRowMock) HasActiveSprites() bool                             { return false }
func (m *frameRowMock) ComposeSpriteBorderRow(int, []byte, func(int) bool) {}
func (m *frameRowMock) TilemapIs80Col() bool                               { return m.wideTM }
func (m *frameRowMock) ComposeWideTilemapRow(y int, _ []byte)              { m.wide = append(m.wide, y) }
func (m *frameRowMock) HiResLayer2Active() bool                            { return m.hiResL2 }
func (m *frameRowMock) Layer2Width() int                                   { return 320 }
func (m *frameRowMock) ComposeWideLayer2Row(y int, _ []byte)               { m.l2 = append(m.l2, y) }

func frameRowULA(t *testing.T, dir string) *ULA {
	t.Helper()
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return New(mem, keyboard.New())
}

func checkFrameRows(t *testing.T, name string, got []int) {
	t.Helper()
	if len(got) != TotalHeight {
		t.Fatalf("%s: %d rows, want %d", name, len(got), TotalHeight)
	}
	if got[0] != 8 || got[TotalHeight-1] != TotalHeight-1+8 {
		t.Errorf("%s: rows %d..%d, want 8..%d (frame rows)", name, got[0], got[TotalHeight-1], TotalHeight-1+8)
	}
}

func TestWideLayerPassesUseFrameRows(t *testing.T) {
	u := frameRowULA(t, "test_roms_frame_rows")

	m := &frameRowMock{tilemap: true}
	u.SetNextCompositor(m)
	u.Render()
	checkFrameRows(t, "border tilemap pass", m.border)

	m = &frameRowMock{wideTM: true}
	u.SetNextCompositor(m)
	u.Render()
	checkFrameRows(t, "80-column tilemap pass", m.wide)

	m = &frameRowMock{hiResL2: true}
	u.SetNextCompositor(m)
	u.Render()
	checkFrameRows(t, "hi-res Layer 2 pass", m.l2)
}
