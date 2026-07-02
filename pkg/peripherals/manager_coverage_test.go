package peripherals

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Peripheral manager wrapper-method coverage: ZX Printer, Kempston
// Mouse, microdrive helpers, Frame, currentTstates, IF1 accessor and
// other convenience methods.

func TestZXPrinter_EnableDisableLifecycle(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if pm.IsZXPrinterEnabled() {
		t.Fatal("printer enabled by default")
	}
	if pm.ZXPrinter() != nil {
		t.Fatal("ZXPrinter() returned non-nil before enable")
	}
	pm.EnableZXPrinter()
	if !pm.IsZXPrinterEnabled() {
		t.Error("printer not enabled after EnableZXPrinter")
	}
	if pm.ZXPrinter() == nil {
		t.Error("ZXPrinter() returned nil after enable")
	}
	pm.EnableZXPrinter() // idempotent
	if !pm.IsZXPrinterEnabled() {
		t.Error("idempotent re-enable should not disable")
	}
	pm.DisableZXPrinter()
	if pm.IsZXPrinterEnabled() {
		t.Error("printer still enabled after Disable")
	}
	if pm.ZXPrinter() != nil {
		t.Error("ZXPrinter() returns non-nil after Disable")
	}
}

func TestKempstonMouse_EnableDisableLifecycle(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if pm.IsKempstonMouseEnabled() {
		t.Fatal("mouse enabled by default")
	}
	pm.EnableKempstonMouse()
	if !pm.IsKempstonMouseEnabled() {
		t.Error("mouse not enabled after EnableKempstonMouse")
	}
	pm.EnableKempstonMouse() // idempotent
	pm.KempstonMouseMove(5, -3)
	pm.KempstonMouseButton(0, true)
	pm.KempstonMouseButton(1, false)
	pm.DisableKempstonMouse()
	if pm.IsKempstonMouseEnabled() {
		t.Error("mouse still enabled after Disable")
	}
	// Move/button while disabled must not panic.
	pm.KempstonMouseMove(1, 1)
	pm.KempstonMouseButton(0, true)
}

func TestFrameHandlesAllStates(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	// Printer disabled — Frame must be a safe no-op.
	pm.Frame()
	pm.EnableZXPrinter()
	pm.Frame() // exercises the zxprinter.Frame() branch
}

func TestCurrentTstates_NilTStates(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	mem.TStates = nil // explicitly clear to hit the nil-guard branch
	pm := NewPeripheralManager(mem, dir)
	if got := pm.currentTstates(); got != 0 {
		t.Errorf("currentTstates with nil TStates = %d, want 0", got)
	}
}

func TestCurrentTstates_NonNil(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	var ts uint64 = 12345
	mem.TStates = &ts
	pm := NewPeripheralManager(mem, dir)
	if got := pm.currentTstates(); got != 12345 {
		t.Errorf("currentTstates = %d, want 12345", got)
	}
}

func TestMicrodriveSlotCount(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	if got := pm.MicrodriveSlotCount(); got != 8 {
		t.Errorf("MicrodriveSlotCount = %d, want 8", got)
	}
}

func TestIF1Methods_NoIF1(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	if pm.IF1() != nil {
		t.Error("IF1() non-nil before enable")
	}
	if pm.IsInterface1Enabled() {
		t.Error("IsInterface1Enabled true before enable")
	}
	if !pm.CanEnableInterface1() {
		t.Error("48K should be able to host IF1")
	}
	// Microdrive ops on nil IF1 are no-ops/errors but must not panic.
	if err := pm.LoadMicrodrive(0, "/no/such/path.mdr"); err == nil {
		t.Error("LoadMicrodrive on disabled IF1 = nil err, want error")
	}
	if err := pm.SaveMicrodrive(0, "/tmp/out.mdr"); err == nil {
		t.Error("SaveMicrodrive on disabled IF1 = nil err, want error")
	}
	if err := pm.InsertMicrodrive(0, nil); err == nil {
		t.Error("InsertMicrodrive on disabled IF1 = nil err, want error")
	}
	pm.EjectMicrodrive(0)                 // must not panic when IF1 is nil
	pm.SetMicrodriveWriteProtect(0, true) // ditto
	if pm.MicrodriveWriteProtected(0) {
		t.Error("MicrodriveWriteProtected without IF1 should be false")
	}
	if pm.MicrodriveCartridgeInserted(0) {
		t.Error("MicrodriveCartridgeInserted without IF1 should be false")
	}
	if val, ok := pm.IF1MemoryRead(0x0000); ok || val != 0 {
		t.Errorf("IF1MemoryRead without IF1 = (%d, %v), want (0, false)", val, ok)
	}
}

func TestInterface2CartridgeName_None(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	if got := pm.Interface2CartridgeName(); got != "" {
		t.Errorf("Interface2CartridgeName with no cart = %q, want \"\"", got)
	}
}

func TestNon48KCannotEnableIF1(t *testing.T) {
	dir := createTestROMDir(t)
	createTestROMs128(t, dir)
	mem, err := memory.New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New 128K: %v", err)
	}
	pm := NewPeripheralManager(mem, dir)
	if pm.CanEnableInterface1() {
		t.Error("128K should not be able to host IF1")
	}
}

func TestLoadPlus3DiskErrors(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	if err := pm.LoadPlus3Disk(0, "/no/such/path.dsk"); err == nil {
		t.Error("LoadPlus3Disk on missing file = nil err, want error")
	}
	if err := pm.SavePlus3Disk(0, "/tmp/should-fail.dsk"); err == nil {
		t.Error("SavePlus3Disk with empty drive = nil err, want error")
	}
}
