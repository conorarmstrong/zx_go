package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// Quick save/load: F2 snapshots the running machine to a single SZX slot under
// the user config dir; F4 restores it. It reuses the same snapshot machinery as
// File → Save/Open, run under withEmulationPaused so it can't race the
// emulation goroutine.

// quickSaveSlotOverride, when non-empty, replaces the config-dir slot path.
// Test-only.
var quickSaveSlotOverride string

// quickSavePath returns the quick-save slot file (an SZX snapshot) under the
// platform config dir, or "" if that dir can't be located.
func quickSavePath() string {
	if quickSaveSlotOverride != "" {
		return quickSaveSlotOverride
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cfg, "zx_go", "quicksave.szx")
}

// quickSaveSupported reports whether the current machine supports SZX
// quick-save/load: the classic Spectrums (48K…+3) and Pentagon. The ZX80/ZX81
// have no ULA/SZX representation, and the Next's full state isn't captured by
// the .szx snapshot.
func (e *emulator) quickSaveSupported() bool {
	return e.ula != nil && e.model != roms.ModelNext
}

// quickSaveState writes the running machine to the quick-save slot.
func (e *emulator) quickSaveState() error {
	if !e.quickSaveSupported() {
		return fmt.Errorf("quick-save is not available for %s", roms.GetModelName(e.model))
	}
	path := quickSavePath()
	if path == "" {
		return fmt.Errorf("could not locate the config directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return e.withEmulationPaused(func() error {
		snap, err := createSnapshotFromEmulator(e)
		if err != nil {
			return err
		}
		return snap.Save(path)
	})
}

// quickLoadState restores the machine from the quick-save slot.
func (e *emulator) quickLoadState() error {
	if !e.quickSaveSupported() {
		return fmt.Errorf("quick-load is not available for %s", roms.GetModelName(e.model))
	}
	path := quickSavePath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no quick-save found — press F2 to save first")
	}
	return e.withEmulationPaused(func() error {
		snap := snapshot.New()
		if err := snap.Load(path); err != nil {
			return err
		}
		return applySnapshotToEmulator(e, snap)
	})
}
