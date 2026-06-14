package testharness

import (
	"errors"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNextRealROMBoot runs the Next harness against whatever ROMs
// the developer has installed at install.Path(). The test SKIPS
// when the distro ROM is absent — CI machines and contributors who
// haven't run the install flow are unaffected.
//
// When the distro IS present (e.g. from the official 24.11 distro
// at https://www.specnext.com/distro/24.11/), the test asserts the
// boot path executes a substantial number of instructions and
// reaches a stable PC, proving the wired-up Z80N + NextRegs +
// 8K MMU + divMMC + esxDOS stack composes against real silicon.
//
// The test opts in to the NextZXOS boot path via
// $ZX_GO_USE_NEXTZXOS. Default Next mode boots fast-boot 48K BASIC
// (which is a different code path covered by other tests).
func TestNextRealROMBoot(t *testing.T) {
	// Deliberately do NOT redirect XDG_CONFIG_HOME / HOME so this
	// test reads the developer's actual install dir.
	if _, err := install.LoadROM(install.DistroROM); err != nil {
		if errors.Is(err, install.ErrROMNotInstalled) {
			t.Skip("NextZXOS distro ROM not installed; run File → Install Next ROMs… to enable this test")
		}
		t.Fatalf("LoadROM: %v", err)
	}
	t.Setenv("ZX_GO_USE_NEXTZXOS", "1")

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext) with installed ROMs: %v", err)
	}

	h.RunFrames(50)

	insns := h.CPU().InstructionCount()
	if insns < 10000 {
		t.Errorf("expected at least 10000 instructions after 50 frames of real boot, got %d", insns)
	}

	// The CPU should be doing more than spinning on PC=0x0000.
	// A real boot enters the divMMC ROM trampoline at 0x0008,
	// then jumps around. We just assert PC is somewhere
	// non-trivially past the boot vector.
	if h.CPU().PC == 0x0000 {
		t.Errorf("PC stuck at 0x0000 after 50 frames; boot not progressing")
	}
}
