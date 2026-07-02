package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// hiResL2Mock is a NextCompositor that reports a hi-res Layer 2 and counts
// the per-row wide-L2 composites the display path drives.
type hiResL2Mock struct {
	width       int
	wideL2Calls int
}

func (m *hiResL2Mock) ComposeScanline(y int, ula, dst []byte)               {}
func (m *hiResL2Mock) HasActiveTilemap() bool                               { return false }
func (m *hiResL2Mock) ComposeBorderRow(y int, dst []byte, f func(int) bool) {}
func (m *hiResL2Mock) HasActiveSprites() bool                               { return false }
func (m *hiResL2Mock) ComposeSpriteBorderRow(y int, dst []byte, f func(int) bool) {
}
func (m *hiResL2Mock) TilemapIs80Col() bool                    { return false }
func (m *hiResL2Mock) ComposeWideTilemapRow(y int, dst []byte) {}
func (m *hiResL2Mock) HiResLayer2Active() bool                 { return true }
func (m *hiResL2Mock) Layer2Width() int                        { return m.width }
func (m *hiResL2Mock) ComposeWideLayer2Row(y int, dst []byte) {
	m.wideL2Calls++
	if y == 0 { // marker at row 0, x 0 — must survive into the frame
		dst[0], dst[1], dst[2], dst[3] = 0xAB, 0xCD, 0xEF, 0xFF
	}
}

// TestRenderHiResLayer2DispatchesWidePass verifies that when Layer 2 is in
// a hi-res mode the display path routes through renderHiResLayer2 and
// composites the wide Layer 2 once per visible row, for both 320 and 640.
func TestRenderHiResLayer2DispatchesWidePass(t *testing.T) {
	testDir := "test_roms_hires_l2"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)
	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	u := New(mem, keyboard.New())

	for _, tc := range []struct {
		name  string
		width int
	}{{"320", 320}, {"640", 640}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &hiResL2Mock{width: tc.width}
			u.SetNextCompositor(m)
			img := u.Render()
			if m.wideL2Calls != TotalHeight {
				t.Errorf("ComposeWideLayer2Row called %d times, want %d", m.wideL2Calls, TotalHeight)
			}
			// The wide-L2 overlay must survive into the returned frame.
			if p := img.Pix[0:4]; p[0] != 0xAB || p[1] != 0xCD || p[2] != 0xEF || p[3] != 0xFF {
				t.Errorf("%s: frame[0,0]=%v, want wide-L2 marker {171 205 239 255}", tc.name, p)
			}
		})
	}
}
