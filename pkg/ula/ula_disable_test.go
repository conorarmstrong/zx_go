package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNextULAOutputDisabledSuppressesScreen pins NR$68 bit7 (Disable ULA
// output): when set, the ULA layer must NOT paint the Spectrum screen. Sonic
// disables the ULA (NR$68=$80) and leaves stale data in screen RAM; ours used
// to render it as a garbled background behind the title because the disable
// was never honoured.
func TestNextULAOutputDisabledSuppressesScreen(t *testing.T) {
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	u := New(mem, keyboard.New())

	// Fill screen RAM: all pixels set, white ink (7) on black paper (0).
	screen := mem.GetPage(mem.ScreenPage)
	for i := 0; i < 0x1800; i++ {
		screen[i] = 0xFF
	}
	for i := 0x1800; i < 0x1B00; i++ {
		screen[i] = 0x07
	}

	// Enabled: the top-left screen pixel renders the white ink.
	img := u.Render()
	on := img.RGBAAt(BorderLeft, BorderTop)
	if on.R == 0 && on.G == 0 && on.B == 0 {
		t.Fatal("ULA enabled: expected white ink at the screen origin, got black")
	}

	// Disabled: the ULA output is off, so the screen pixel must not be the
	// white ink — it falls back to the disabled fill (black with no compositor).
	u.SetULAOutputDisabled(true)
	img = u.Render()
	off := img.RGBAAt(BorderLeft, BorderTop)
	if off.R == on.R && off.G == on.G && off.B == on.B {
		t.Fatalf("ULA output disabled but screen still shows the ULA ink $%02X%02X%02X", off.R, off.G, off.B)
	}
}
