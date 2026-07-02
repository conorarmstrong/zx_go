package testharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestModelNextLayer2VisibleEndToEnd is the end-to-end pixel check
// for the Layer 2 render path. Configure NextRegs through the
// dispatcher exactly as guest software would:
//   - select Layer 2's first palette
//   - upload an entry at index 5 = pure red
//   - configure Layer 2 bank + enable
//   - write a row of palette-index-5 bytes into the Layer 2 frame
//   - call ScreenImage() and verify the active-display row is red
//
// If any link in the chain (palette write path, Layer 2 enable bit,
// compositor integration, ULA overlay pass) breaks, this test
// fails — the unit tests for each component alone wouldn't catch
// a wiring regression at the boundaries.
func TestModelNextLayer2VisibleEndToEnd(t *testing.T) {
	installtest.RedirectConfig(t)
	installtest.AssertSandboxed(t)

	// Install a minimal fake distro ROM so memory.New(ModelNext)
	// succeeds.
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), make([]byte, 0x4000), 0644); err != nil {
		t.Fatal(err)
	}

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}

	// Pre-fill the screen RAM bank with attributes that show
	// background (paper colour 7 = white): every byte 0 below
	// 0x1800 produces "all-paper" pixels; attribute byte 0x38
	// gives white paper.
	screenBank := h.MemoryBus().GetPage(5)
	for i := 0; i < 0x1800; i++ {
		screenBank[i] = 0x00
	}
	for i := 0x1800; i < 0x1B00; i++ {
		screenBank[i] = 0x38
	}

	// Configure NextRegs via the dispatcher.
	mem := h.MemoryBus()
	mem.SetMMU(0, 0xFF) // ensure ROM at slot 0
	// (Direct mem writes used here in place of the awkward
	// post-RST-8 register dance; the dispatcher methods are
	// exercised separately. End-to-end here means "harness
	// stack composes correctly".)

	// Use the ULA + harness machinery to drive a few NextReg
	// writes. We can't easily reach the dispatcher from the
	// harness's public API, so the test instead validates a
	// state we install via the host side: set Layer 2 bank,
	// enable Layer 2, install a palette entry, paint a row.
	//
	// This is the most direct way to assert "the ULA's render
	// path consults the compositor" without simulating real
	// RST 8 / port-write sequences.

	// Pick RAM bank 1 to hold Layer 2 (avoiding bank 5 which
	// holds screen RAM). Fill row 0 of bank 1 with palette
	// index 5.
	layerBank := mem.GetPage(1)
	for i := 0; i < 256; i++ {
		layerBank[i] = 5
	}

	// Now we need to: enable Layer 2, set its active bank to 1,
	// set palette entry 5 = pure red. The dispatcher is reached
	// via port 0x243B (select) and 0x253B (data) through ULA's
	// WritePort. WritePort is a public method.
	writeReg := func(reg, val byte) {
		h.ULA().WritePort(0x243B, reg)
		h.ULA().WritePort(0x253B, val)
	}

	// NextReg 0x12: Layer 2 active bank
	writeReg(0x12, 1)
	// NextReg 0x69 bit 7: Layer 2 enable
	writeReg(0x69, 0x80)
	// NextReg 0x43: select Layer 2 First palette as write target.
	// Per FPGA zxnext.vhd bits 6:4 = wsel; the L2+Sprite SRAM and
	// ULA+Tilemap SRAM split via (wsel(0) XOR wsel(1)). wsel = 1
	// targets L2 First (and matches NextBASIC documentation).
	writeReg(0x43, byte(1<<4))
	// NextReg 0x40: palette index = 5
	writeReg(0x40, 5)
	// NextReg 0x41: palette value = pure red (high 8 bits of 9-bit:
	// 0b1110_0000, shifted left 1 in Write8 to give 0b1_1100_0000_0).
	// Actually we want the entry to encode RRR=111 GGG=000 BBB=000
	// in the 9-bit form. The 9-bit value is 0b1_1100_0000 = 0x1C0.
	// Write8 shifts the 8-bit input left by 1, so input 0xE0
	// becomes 9-bit 0x1C0 = our red.
	writeReg(0x41, 0xE0)

	// Render two frames so any pending state settles.
	h.RunFrames(2)
	img := h.ScreenImage()

	// Active display row 0 in img coordinates is BorderTop, x in
	// [BorderLeft, BorderLeft+256). The bank we set for Layer 2
	// has palette-index-5 in row 0. With the compositor enabled,
	// this row should now be pure red.
	const borderLeft = 32
	const borderTop = 24
	r, g, b, a := img.RGBAAt(borderLeft+0, borderTop+0).RGBA()
	r >>= 8
	g >>= 8
	b >>= 8
	a >>= 8
	if r == 0 || g != 0 || b != 0 {
		t.Errorf("active pixel (0,0): rgb=%d/%d/%d, want pure red (Layer 2 over ULA)", r, g, b)
	}
	if a != 0xFF {
		t.Errorf("active pixel alpha = %d, want 0xFF", a)
	}
}
