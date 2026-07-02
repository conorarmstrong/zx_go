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

// TestNextSpritePortWritesRouteToSpriteEngine exercises the
// end-to-end sprite upload path: port $303B selects a sprite slot
// and pattern-RAM cursor, $57 streams attribute bytes, and $5B
// streams pattern bytes. This is the integration counterpart to the
// unit tests in pkg/next/sprite — it proves the ULA port dispatch
// actually reaches the sprite engine the compositor renders from,
// not just that the engine itself decodes bytes correctly.
func TestNextSpritePortWritesRouteToSpriteEngine(t *testing.T) {
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

	// Select sprite slot 3 (also sets the pattern-RAM cursor to
	// 3*256 = 768).
	h.ULA().WritePort(0x303B, 0x03)

	// Stream attribute bytes 0 (X LSB) and 1 (Y) via port $57.
	h.ULA().WritePort(0x0057, 0x2A) // X = 0x2A
	h.ULA().WritePort(0x0057, 0x10) // Y = 0x10

	// Stream two pattern bytes via port $5B.
	h.ULA().WritePort(0x005B, 0xAB)
	h.ULA().WritePort(0x005B, 0xCD)

	sprites := h.NextSprites()
	if sprites == nil {
		t.Fatal("NextSprites() = nil; sprite engine not wired on ModelNext harness")
	}
	sp := sprites.Sprite(3)
	if sp == nil {
		t.Fatal("Sprite(3) = nil")
	}
	if sp.X != 0x2A {
		t.Errorf("sprite 3 X = %#x, want 0x2A", sp.X)
	}
	if sp.Y != 0x10 {
		t.Errorf("sprite 3 Y = %#x, want 0x10", sp.Y)
	}
	if got := sprites.PatternByte(768); got != 0xAB {
		t.Errorf("pattern[768] = %#x, want 0xAB", got)
	}
	if got := sprites.PatternByte(769); got != 0xCD {
		t.Errorf("pattern[769] = %#x, want 0xCD", got)
	}
}

// TestNextI2CPortClaimedByRTCBus verifies port $113B (i2c SDA
// read-back for the bit-banged DS1307 RTC) is claimed by the ULA
// port dispatch once the harness wires the RTC bus. Without this
// wiring NextZXOS's menu clock read fails every frame (the read
// falls through instead of returning the idle-high SDA level).
func TestNextI2CPortClaimedByRTCBus(t *testing.T) {
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

	val, ok := h.ULA().ReadPort(0x113B)
	if !ok {
		t.Fatal("port 0x113B not claimed — RTC i2c bus not wired")
	}
	if val&0x01 == 0 {
		t.Errorf("SDA idle level = 0, want 1 (bus idles high)")
	}
}
