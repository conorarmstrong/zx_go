package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// Quick save/load: F2 snapshots the running machine to a single slot under the
// user config dir; F4 restores it. Both run under withEmulationPaused so they
// cannot race the emulation goroutine.
//
// There are two formats, and which one is used is decided by what can actually
// represent the machine.
//
// The classic Spectrums get SZX, the same as File → Save, because it is a
// portable format other emulators read. .sna, .z80 and .szx all describe a
// 48K/128K memory map and a Z80, though, and there is nowhere in any of them to
// put the Next's 2 MB and its NextRegs, the SAM's LMPR/HMPR/VMPR paging and
// SAA1099, or the ZX81's CPU-generated display. Those machines were refused
// outright rather than saved wrongly, which was the right call with only SZX
// available.
//
// Each of them does have a machinestate registry — the one the rewind ring
// already captures every frame — so they now save from that. It is our own
// format and deliberately not portable: it holds each device's own encoding, so
// it is a save state for this emulator rather than an interchange file.

// quickSaveSlotOverride, when non-empty, replaces the config-dir slot path.
// Test-only.
var quickSaveSlotOverride string

// quickSavePath returns the quick-save slot file under the platform config dir,
// or "" if that dir can't be located.
//
// The two formats use different extensions so one cannot be handed to the other
// by accident, and so a user finding the file knows what it is.
func (e *emulator) quickSavePath() string {
	if quickSaveSlotOverride != "" {
		return quickSaveSlotOverride
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	name := "quicksave.szx"
	if !e.snapshotQuickSave() {
		name = "quicksave.zxgostate"
	}
	return filepath.Join(cfg, "zx_go", name)
}

// snapshotQuickSave reports whether this machine's quick-save goes through the
// portable SZX path. Everything else goes through the registry.
func (e *emulator) snapshotQuickSave() bool {
	return e.ula != nil && e.model != roms.ModelNext
}

// quickSaveSupported reports whether quick-save works at all on this machine.
//
// It is now true everywhere with something to save, which is every machine the
// emulator builds. It stays a function rather than a constant because a machine
// whose registry is empty has nothing to write, and saying so beats writing a
// file that restores nothing.
func (e *emulator) quickSaveSupported() bool {
	if e.snapshotQuickSave() {
		return true
	}
	return e.stateRegistry().Len() > 0
}

// quickSaveState writes the running machine to the quick-save slot.
func (e *emulator) quickSaveState() error {
	if !e.quickSaveSupported() {
		return fmt.Errorf("quick-save is not available for %s", roms.GetModelName(e.model))
	}
	path := e.quickSavePath()
	if path == "" {
		return fmt.Errorf("could not locate the config directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return e.withEmulationPaused(func() error {
		if !e.snapshotQuickSave() {
			return e.saveRegistryState(path)
		}
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
	path := e.quickSavePath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no quick-save found — press F2 to save first")
	}
	return e.withEmulationPaused(func() error {
		if !e.snapshotQuickSave() {
			return e.loadRegistryState(path)
		}
		snap := snapshot.New()
		if err := snap.Load(path); err != nil {
			return err
		}
		return applySnapshotToEmulator(e, snap)
	})
}

// saveRegistryState writes a machinestate capture to path, tagged with the
// machine it came from.
//
// The write goes to a temporary file first and is renamed into place, so an
// interrupted save leaves the previous slot intact rather than a half-written
// file that looks like a save state until it is loaded.
func (e *emulator) saveRegistryState(path string) error {
	blob := machinestate.Encode(roms.GetModelName(e.model), e.stateRegistry().Capture())
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return fmt.Errorf("write save state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace save state: %w", err)
	}
	return nil
}

// loadRegistryState restores a machinestate capture from path.
//
// The machine tag is checked before the state is applied, so loading a Next
// state into a SAM says which machine the file came from. Registry.Restore
// would refuse it anyway, on the device set — but as a list of device names,
// which is accurate and tells the user nothing they can act on.
func (e *emulator) loadRegistryState(path string) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read save state: %w", err)
	}
	machine, state, err := machinestate.Decode(blob)
	if err != nil {
		return err
	}
	if want := roms.GetModelName(e.model); machine != want {
		return fmt.Errorf("this save state is from a %s, and this is a %s", machine, want)
	}
	return e.stateRegistry().Restore(state)
}
