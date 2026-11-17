package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// hiResL2Mock is a NextCompositor that reports a 320x256 or 640x256 Layer 2
// and records the frame rows the display path composes.
type hiResL2Mock struct {
	stubCompositor
	scale int
	rows  []int
}

func (m *hiResL2Mock) HiResLayer2Active() bool { return true }
func (m *hiResL2Mock) WideScale() int          { return m.scale }
func (m *hiResL2Mock) ComposeFrameRowRange(fy int, ula, dst []byte, scale, x0, x1 int) {
	m.rows = append(m.rows, fy)
	if fy == 0 { // marker at frame row 0, x 0; it must survive into the frame
		dst[0], dst[1], dst[2], dst[3] = 0xAB, 0xCD, 0xEF, 0xFF
	}
}

// A 320x256 or 640x256 Layer 2 has 256 lines, so the display returns the full
// frame, every row composed once, at the width the layer needs.
func TestRenderHiResLayer2ComposesTheFullFrame(t *testing.T) {
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
		scale int
	}{{"320", 1}, {"640", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &hiResL2Mock{scale: tc.scale}
			u.SetNextCompositor(m)
			img := u.Render()
			if len(m.rows) != 256 {
				t.Errorf("composed %d rows, want 256", len(m.rows))
			}
			for i, fy := range m.rows {
				if fy != i {
					t.Fatalf("row %d composed as frame row %d, want %d", i, fy, i)
				}
			}
			if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != TotalWidth*tc.scale || h != 256 {
				t.Errorf("frame %dx%d, want %dx256", w, h, TotalWidth*tc.scale)
			}
			if p := img.Pix[0:4]; p[0] != 0xAB || p[1] != 0xCD || p[2] != 0xEF || p[3] != 0xFF {
				t.Errorf("frame[0,0]=%v, want the marker {171 205 239 255}", p)
			}
		})
	}
}
