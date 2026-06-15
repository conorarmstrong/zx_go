package zx8x

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func inkCount(m *Machine) int {
	img := m.Image()
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r == 0 {
				n++
			}
		}
	}
	return n
}

// Boot the real ZX81 ROM headless and confirm it renders (the cold boot shows
// an inverse-K cursor near the bottom of an otherwise blank screen).
func TestZX81BootsAndRenders(t *testing.T) {
	m, err := NewMachine(roms.ModelZX81, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		m.RunFrame()
	}
	ink := inkCount(m)
	t.Logf("PC=%04X I=%02X ink=%d", m.CPU.PC, m.CPU.I, ink)
	if f, e := os.Create(filepath.Join(t.TempDir(), "zx81_boot.png")); e == nil {
		_ = png.Encode(f, m.Image())
		_ = f.Close()
	}
	if ink == 0 {
		t.Errorf("screen blank after 250 frames — ZX81 did not render")
	}
}

func TestZX81InjectPRunsWithoutPanic(t *testing.T) {
	m, _ := NewMachine(roms.ModelZX81, "")
	for i := 0; i < 100; i++ {
		m.RunFrame()
	}
	// A minimal .P payload (system-variable area is normally larger; this just
	// exercises the inject+resume path without crashing).
	m.InjectP(make([]byte, 0x200))
	for i := 0; i < 50; i++ {
		m.RunFrame()
	}
	t.Logf("after InjectP: PC=%04X", m.CPU.PC)
}
