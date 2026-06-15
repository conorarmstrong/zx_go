package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Regression: switching to the ZX80/ZX81 must leave emu.peripherals non-nil.
// saveConfig() (run on every menu action, including the model switch itself)
// calls emu.peripherals.IsDiscipleEnabled() unconditionally, so a nil manager
// panics the GUI the moment you pick Machine → Sinclair ZX81.
func TestZX8xEmulatorHasInertPeripherals(t *testing.T) {
	for _, m := range []roms.SpectrumModel{roms.ModelZX81, roms.ModelZX80} {
		e, err := newEmulator(m)
		if err != nil {
			t.Fatalf("newEmulator(%s): %v", roms.GetModelName(m), err)
		}
		if e.zx8x == nil {
			t.Errorf("%s: zx8x machine is nil", roms.GetModelName(m))
		}
		if e.peripherals == nil {
			t.Fatalf("%s: peripherals nil — saveConfig would panic on switch", roms.GetModelName(m))
		}
		if e.ula != nil {
			t.Errorf("%s: ula should be nil (ZX8x has no ULA)", roms.GetModelName(m))
		}
		// The exact calls saveConfig makes — must not panic.
		_ = e.peripherals.IsDiscipleEnabled()
		_ = e.peripherals.IsMultifaceEnabled()
		// And the render path must work without a ULA.
		if e.renderFrame() == nil {
			t.Errorf("%s: renderFrame returned nil", roms.GetModelName(m))
		}
	}
}
