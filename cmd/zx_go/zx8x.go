package main

import (
	"image"
	"log/slog"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/zx8x"
)

// isZX8x reports whether a model is a Sinclair ZX80 or ZX81.
func isZX8x(model roms.SpectrumModel) bool {
	return model == roms.ModelZX80 || model == roms.ModelZX81
}

// newZX8xEmulator builds an emulator running the Sinclair ZX80 or ZX81. These
// machines generate the display from the CPU itself (see pkg/zx8x): there is no
// ULA, peripheral manager or Spectrum-style paging, so the emulator's ula and
// peripherals fields stay nil and the render loop reads zx8x.Image() instead.
func newZX8xEmulator(model roms.SpectrumModel) (*emulator, error) {
	m, err := zx8x.NewMachine(model, "roms")
	if err != nil {
		return nil, err
	}
	if path := userKeymapPath(); path != "" {
		if err := m.KB.LoadOverrides(path); err != nil {
			// non-fatal: fall back to the default ZX8x layout
			slog.Warn("failed to load custom keymap", "err", err)
		}
	}
	// An inert peripheral manager: the ZX80/ZX81 have no Spectrum-bus
	// peripherals, but the GUI's menu-state and config-save code reads
	// emu.peripherals.IsXxxEnabled() unconditionally, so a non-nil manager
	// (everything reporting disabled) keeps those paths safe. It is not wired
	// into the ZX8x memory map, so it stays inert.
	pm := peripherals.NewPeripheralManager(m.Mem, "roms")

	e := &emulator{
		cpu:          m.CPU,
		mem:          m.Mem,
		kbd:          m.KB,
		zx8x:         m,
		peripherals:  pm,
		model:        model,
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10),
		stopChan:     make(chan struct{}),
	}
	e.paused.Store(true)
	return e, nil
}

// renderFrame returns the current display image for any machine type. ZX80/ZX81
// produce a fresh image per presented frame; the Spectrum/Next path renders the
// ULA composite in place.
func (e *emulator) renderFrame() *image.RGBA {
	if e.zx8x != nil {
		return e.zx8x.Image()
	}
	if e.sam != nil {
		return e.sam.Render()
	}
	return e.ula.Render()
}
