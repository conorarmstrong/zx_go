package testharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// realNEXSampleDir is where developers stage real distro .NEX
// samples for the smoke test. Populated manually via:
//
//	unzip -j -o sn-complete-24.11.zip 'apps/dev/BBCBasic/bbcbasic.nex' \
//	  -d /tmp/nex-samples/
//
// CI skips this test when the directory isn't present.
const realNEXSampleDir = "/tmp/nex-samples"

// TestLoadNEXAgainstRealSample loads one of the official distro
// .NEX files through the wired-up harness and confirms the bank
// contents landed where expected. Skips if the sample isn't
// available, so CI without distro files still passes.
func TestLoadNEXAgainstRealSample(t *testing.T) {
	sample := filepath.Join(realNEXSampleDir, "bbcbasic.nex")
	if _, err := os.Stat(sample); os.IsNotExist(err) {
		t.Skipf("%s not present; extract from the SpecNext distro to enable", sample)
	}

	installtest.RedirectConfig(t)
	installFakeDistroForLoad(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}

	if err := h.LoadNEX(sample); err != nil {
		t.Fatalf("LoadNEX: %v", err)
	}

	// bbcbasic.nex sets PC = 0x5b00, SP = 0.
	if h.CPU().PC != 0x5b00 {
		t.Errorf("PC after LoadNEX = %#x, want 0x5b00", h.CPU().PC)
	}
	if h.CPU().SP != 0x0000 {
		t.Errorf("SP after LoadNEX = %#x, want 0x0000", h.CPU().SP)
	}

	// bbcbasic.nex's executable code starts at PC=0x5b00 with
	// "DI ; LD SP,$0000" — i.e. opcodes F3 31 00 00. Address
	// 0x5b00 falls in bank 5 (which maps at 0x4000 on the default
	// 48K-equivalent layout), at bank offset 0x1b00. The file
	// places those bytes at file offset 0x1d00 (header 0x200 +
	// bank-5 offset 0x1b00). Spot-checking these four bytes
	// proves the loader did the bank → memory walk correctly.
	pc := h.CPU().PC
	want := []byte{0xF3, 0x31, 0x00, 0x00}
	for i, w := range want {
		if got := h.MemoryBus().Read(pc + uint16(i)); got != w {
			t.Errorf("byte at %#x = %#02x, want %#02x", pc+uint16(i), got, w)
		}
	}
}
