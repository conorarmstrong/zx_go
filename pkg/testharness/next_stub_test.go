package testharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// TestNewModelNextSucceedsWithoutDistro confirms that ModelNext
// boots cleanly even when the optional NextZXOS distro ROM is not
// installed. The Next mode boots through the embedded 128K BASIC
// ROMs in that case; the distro is only required for code paths
// that page in bank-2/3 NextZXOS extensions.
func TestNewModelNextSucceedsWithoutDistro(t *testing.T) {
	installtest.RedirectConfig(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext) without distro: %v", err)
	}
	if h == nil {
		t.Fatal("nil harness from successful New")
	}
	if h.CPU().Variant != z80.VariantZ80N {
		t.Errorf("CPU variant = %v, want VariantZ80N", h.CPU().Variant)
	}
}

// TestNextHarnessIntegrationSmoke runs the wired-up Next harness for
// a small number of frames against a synthetic ROM. With the
// divMMC pager active the CPU reads NOPs from zeroed RAM, so PC
// just walks the address space — but T-states must advance and
// the wiring must not panic, which proves the components compose.
// This is the closest we can get to a boot smoke test without a
// real distro ROM.
func TestNextHarnessIntegrationSmoke(t *testing.T) {
	installtest.RedirectConfig(t)
	installtest.AssertSandboxed(t)
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic 16K distro ROM with all zeros (NOPs). When the
	// divMMC pager is paged in (default after CPU reset hits
	// PC=0x0000), reads come from the zeroed divMMC RAM, not the
	// ROM. Either way the byte is zero -> NOP.
	rom := make([]byte, 0x4000)
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatal(err)
	}

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}

	// Tstates() wraps per frame, so InstructionCount is the
	// monotonic signal that "the CPU actually ran".
	startInsns := h.CPU().InstructionCount()
	h.RunFrames(2)
	if h.CPU().InstructionCount() <= startInsns {
		t.Errorf("instruction count did not advance over 2 frames (start=%d end=%d)",
			startInsns, h.CPU().InstructionCount())
	}
}

func TestNewModelNextSucceedsWithROMs(t *testing.T) {
	installtest.RedirectConfig(t)

	// Install a fake 16K distro ROM at the install location.
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	rom := make([]byte, 0x4000)
	for i := range rom {
		rom[i] = byte(i & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatal(err)
	}

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext) with installed ROMs: %v", err)
	}
	if h == nil {
		t.Fatal("nil harness from successful New")
	}

	if h.CPU().Variant != z80.VariantZ80N {
		t.Errorf("CPU variant = %v, want VariantZ80N", h.CPU().Variant)
	}

	// Smoke: NextReg port routing wired. Write to 0x243B (select)
	// then read from 0x253B should round-trip whatever's stored at
	// the selected register (default 0 on power-on).
	h.ULA().WritePort(0x243B, 0x40) // select palette index reg
	val, ok := h.ULA().ReadPort(0x253B)
	if !ok {
		t.Errorf("port 0x253B should be claimed by NextReg dispatcher")
	}
	if val != 0 {
		t.Errorf("default register read should be 0, got %#x", val)
	}
}

// TestNextDACPortWritesRouteToDACBank exercises the end-to-end DAC
// dispatch path. A guest OUT to one of the seven documented DAC
// ports must reach the bank and update the per-channel level, then
// MixInto must produce the expected contribution. This is the
// integration counterpart to the unit tests in pkg/next/dac.
func TestNextDACPortWritesRouteToDACBank(t *testing.T) {
	installtest.RedirectConfig(t)
	installtest.AssertSandboxed(t)
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	rom := make([]byte, 0x4000)
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatal(err)
	}

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}

	// Channel A via port 0x0F (alias of 0xF1).
	h.ULA().WritePort(0x0F, 0xC0)
	// Channel D via port 0xFB.
	h.ULA().WritePort(0xFB, 0x80)

	// Verify the contribution to a 4-sample buffer. Channel A at
	// 0xC0 (192), Channel D at 0x80 (128), others at 0 → mean =
	// (192+0+0+128)/4 = 80. Centred = 80 - 128 = -48. Contrib =
	// -48 * 64 = -3072.
	buf := []int16{0, 0, 0, 0}
	h.NextDAC().MixInto(buf)
	const wantContrib int16 = -3072
	for i, v := range buf {
		if v != wantContrib {
			t.Errorf("DAC mix contrib buf[%d] = %d, want %d", i, v, wantContrib)
		}
	}
}
