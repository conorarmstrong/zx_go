package peripherals

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// Covers the IF1-enabled microdrive paths in PeripheralManager; the
// disabled-IF1 nil-guard branches are covered separately.

func enabledIF1Manager(t *testing.T) *PeripheralManager {
	t.Helper()
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	romBytes := make([]byte, 8192)
	if err := pm.EnableInterface1(romBytes); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}
	return pm
}

func TestInsertEjectMicrodrive_WithIF1(t *testing.T) {
	pm := enabledIF1Manager(t)
	cart := microdrive.New(180)
	if err := pm.InsertMicrodrive(0, cart); err != nil {
		t.Fatalf("InsertMicrodrive: %v", err)
	}
	if !pm.MicrodriveCartridgeInserted(0) {
		t.Error("CartridgeInserted = false after Insert")
	}
	pm.EjectMicrodrive(0)
	if pm.MicrodriveCartridgeInserted(0) {
		t.Error("CartridgeInserted = true after Eject")
	}
}

func TestSetMicrodriveWriteProtect_WithIF1(t *testing.T) {
	pm := enabledIF1Manager(t)
	cart := microdrive.New(180)
	if err := pm.InsertMicrodrive(0, cart); err != nil {
		t.Fatal(err)
	}
	pm.SetMicrodriveWriteProtect(0, true)
	if !pm.MicrodriveWriteProtected(0) {
		t.Error("WriteProtected = false after Set(true)")
	}
	pm.SetMicrodriveWriteProtect(0, false)
	if pm.MicrodriveWriteProtected(0) {
		t.Error("WriteProtected = true after Set(false)")
	}
}

func TestSaveLoadMicrodrive_WithIF1(t *testing.T) {
	pm := enabledIF1Manager(t)
	cart := microdrive.New(180)
	_ = pm.InsertMicrodrive(0, cart)

	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "test.mdr")
	if err := pm.SaveMicrodrive(0, savePath); err != nil {
		t.Fatalf("SaveMicrodrive: %v", err)
	}

	// Load back into a different drive slot.
	if err := pm.LoadMicrodrive(1, savePath); err != nil {
		t.Fatalf("LoadMicrodrive: %v", err)
	}
	if !pm.MicrodriveCartridgeInserted(1) {
		t.Error("drive 1 has no cartridge after Load")
	}
}

func TestSaveMicrodrive_EmptyDriveReturnsError(t *testing.T) {
	pm := enabledIF1Manager(t)
	tmpDir := t.TempDir()
	err := pm.SaveMicrodrive(0, filepath.Join(tmpDir, "empty.mdr"))
	if err == nil {
		t.Error("Save on empty drive should return error")
	}
}

func TestLoadMicrodrive_BadPathReturnsError(t *testing.T) {
	pm := enabledIF1Manager(t)
	if err := pm.LoadMicrodrive(0, "/no/such/file.mdr"); err == nil {
		t.Error("Load with bad path should return error")
	}
}
