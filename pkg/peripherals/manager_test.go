package peripherals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// createTestROMDir builds a temporary directory with all the ROM files that
// memory.New and the peripheral subsystems need.
func createTestROMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// System ROMs (16KB)
	systemROMs := []string{"48.rom"}
	for _, name := range systemROMs {
		data := make([]byte, 16384)
		for i := range data {
			data[i] = byte(i & 0xFF)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Peripheral ROMs (8KB)
	peripheralROMs := []string{
		"gdos.rom",
		"mf1_official.rom",
		"mf128_official.rom",
		"mf3_official.rom",
	}
	for _, name := range peripheralROMs {
		data := make([]byte, 8192)
		for i := range data {
			data[i] = byte((i + 0x10) & 0xFF)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	return dir
}

// newTestMemory creates a *memory.Memory suitable for testing.
func newTestMemory(t *testing.T, dir string) *memory.Memory {
	t.Helper()
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New failed: %v", err)
	}
	return mem
}

// --- Creation ---

func TestNewPeripheralManager(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)

	pm := NewPeripheralManager(mem, dir)
	if pm == nil {
		t.Fatal("NewPeripheralManager returned nil")
	}
	if pm.IsDiscipleEnabled() {
		t.Error("Disciple should not be enabled by default")
	}
	if pm.IsMultifaceEnabled() {
		t.Error("Multiface should not be enabled by default")
	}
}

// --- Disciple enable / disable ---

func TestEnableDisciple(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if err := pm.EnableDisciple(dir); err != nil {
		t.Fatalf("EnableDisciple failed: %v", err)
	}
	if !pm.IsDiscipleEnabled() {
		t.Error("Disciple should be enabled after EnableDisciple")
	}
	if pm.GetDisciple() == nil {
		t.Error("GetDisciple should not return nil after enabling")
	}
}

func TestEnableDisciple_AlreadyEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)
	// Calling again should be a no-op (no error)
	if err := pm.EnableDisciple(dir); err != nil {
		t.Fatalf("second EnableDisciple should not fail: %v", err)
	}
}

func TestDisableDisciple(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)
	pm.DisableDisciple()

	if pm.IsDiscipleEnabled() {
		t.Error("Disciple should be disabled after DisableDisciple")
	}
	if pm.GetDisciple() != nil {
		t.Error("GetDisciple should return nil after disabling")
	}
}

// --- Multiface enable / disable ---

func TestEnableMultiface(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if err := pm.EnableMultiface(multiface.Multiface1, dir); err != nil {
		t.Fatalf("EnableMultiface(MF1) failed: %v", err)
	}
	if !pm.IsMultifaceEnabled() {
		t.Error("Multiface should be enabled")
	}
	if pm.GetMultiface() == nil {
		t.Error("GetMultiface should not return nil")
	}
}

func TestEnableMultiface_AlreadyEnabled_SameVariant(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface128, dir)
	// Same variant again should be a no-op
	if err := pm.EnableMultiface(multiface.Multiface128, dir); err != nil {
		t.Fatalf("re-enabling same variant should not fail: %v", err)
	}
}

func TestEnableMultiface_SwitchVariant(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)
	// Switching to a different variant should succeed
	if err := pm.EnableMultiface(multiface.Multiface128, dir); err != nil {
		t.Fatalf("switching Multiface variant failed: %v", err)
	}
	if !pm.IsMultifaceEnabled() {
		t.Error("Multiface should still be enabled after variant switch")
	}
}

func TestDisableMultiface(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)
	pm.DisableMultiface()

	if pm.IsMultifaceEnabled() {
		t.Error("Multiface should be disabled")
	}
	if pm.GetMultiface() != nil {
		t.Error("GetMultiface should return nil after disabling")
	}
}

// --- Port I/O when peripherals disabled ---

func TestHandlePortRead_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_, handled := pm.HandlePortRead(0x1F) // Disciple control port
	if handled {
		t.Error("port read should not be handled with no peripherals enabled")
	}
}

func TestHandlePortWrite_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	handled := pm.HandlePortWrite(0x1F, 0x00)
	if handled {
		t.Error("port write should not be handled with no peripherals enabled")
	}
}

// --- Port I/O with Disciple enabled ---

func TestHandlePortRead_DiscipleEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)

	// Disciple FDC status port (0x1B)
	_, handled := pm.HandlePortRead(0x1B)
	if !handled {
		t.Error("Disciple status port should be handled when Disciple is enabled")
	}
}

func TestHandlePortWrite_DiscipleEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)

	handled := pm.HandlePortWrite(0x5B, 0x10) // FDC Track register
	if !handled {
		t.Error("Disciple track port write should be handled")
	}
}

// --- Port I/O with Multiface enabled ---

func TestHandlePortRead_MultifaceEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)

	// MF1 port mask: (port & 0x0072) == 0x0012. Port 0x009F matches with A7=1.
	_, handled := pm.HandlePortRead(0x009F)
	if !handled {
		t.Error("Multiface port should be handled when enabled")
	}
}

func TestHandlePortWrite_MultifaceEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)

	// MF1 port mask: (port & 0x0072) == 0x0012. Port 0x009F matches.
	handled := pm.HandlePortWrite(0x009F, 0x00)
	if !handled {
		t.Error("Multiface port write should be handled when enabled")
	}
}

// --- HandleNMI ---

func TestHandleNMI_NoMultiface(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if pm.HandleNMI() {
		t.Error("HandleNMI should return false with no Multiface")
	}
}

func TestHandleNMI_MultifaceEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)

	if !pm.HandleNMI() {
		t.Error("HandleNMI should return true with Multiface enabled")
	}
}

// --- GetStatus ---

func TestGetStatus_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	status := pm.GetStatus()
	if status["disciple_enabled"] != false {
		t.Error("disciple_enabled should be false")
	}
	if status["multiface_enabled"] != false {
		t.Error("multiface_enabled should be false")
	}

	// Fields that only appear when peripheral is enabled should be absent
	if _, ok := status["disciple_rom_paged"]; ok {
		t.Error("disciple_rom_paged should not be present when disabled")
	}
	if _, ok := status["multiface_variant"]; ok {
		t.Error("multiface_variant should not be present when disabled")
	}
}

func TestGetStatus_AllEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)
	_ = pm.EnableMultiface(multiface.Multiface128, dir)

	status := pm.GetStatus()

	if status["disciple_enabled"] != true {
		t.Error("disciple_enabled should be true")
	}
	if status["multiface_enabled"] != true {
		t.Error("multiface_enabled should be true")
	}
	if _, ok := status["disciple_rom_paged"]; !ok {
		t.Error("disciple_rom_paged should be present")
	}
	if _, ok := status["disciple_inhibited"]; !ok {
		t.Error("disciple_inhibited should be present")
	}
	if status["multiface_variant"] != "Multiface 128" {
		t.Errorf("multiface_variant = %v, want 'Multiface 128'", status["multiface_variant"])
	}
	if _, ok := status["multiface_rom_paged"]; !ok {
		t.Error("multiface_rom_paged should be present")
	}
	if _, ok := status["multiface_invisible"]; !ok {
		t.Error("multiface_invisible should be present")
	}
	if _, ok := status["multiface_red_button"]; !ok {
		t.Error("multiface_red_button should be present")
	}
}

// --- GetCompatibleMultifaceVariant ---

func TestGetCompatibleMultifaceVariant(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	tests := []struct {
		model roms.SpectrumModel
		want  multiface.MultifaceType
	}{
		{roms.Model48K, multiface.Multiface1},
		{roms.Model128K, multiface.Multiface128},
		{roms.ModelPlus2, multiface.Multiface128},
		{roms.ModelPlus2A, multiface.Multiface128},
		{roms.ModelPlus3, multiface.Multiface3},
	}
	for _, tc := range tests {
		got := pm.GetCompatibleMultifaceVariant(tc.model)
		if got != tc.want {
			t.Errorf("GetCompatibleMultifaceVariant(%d) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

// --- HandleMemoryRead ---

func TestHandleMemoryRead_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_, handled := pm.HandleMemoryRead(0x0000)
	if handled {
		t.Error("memory read should not be intercepted with no peripherals")
	}
}

func TestHandleMemoryRead_MultifaceROMPaged(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)
	// Trigger NMI to page in Multiface ROM
	pm.HandleNMI()

	mf := pm.GetMultiface()
	if !mf.IsROMPaged() {
		t.Fatal("Multiface ROM should be paged in after NMI")
	}

	// Reading from 0x0000-0x1FFF should be intercepted
	val, handled := pm.HandleMemoryRead(0x0000)
	if !handled {
		t.Error("memory read at 0x0000 should be intercepted when Multiface ROM is paged in")
	}
	// The value should come from the Multiface ROM
	expectedROM := mf.GetROM()
	if val != expectedROM[0] {
		t.Errorf("memory read value = 0x%02X, want 0x%02X", val, expectedROM[0])
	}

	// Address 0x2000-0x3FFF should be intercepted as Multiface RAM
	_, handled = pm.HandleMemoryRead(0x2000)
	if !handled {
		t.Error("memory read at 0x2000 should be intercepted by Multiface RAM")
	}

	// Address >= 0x4000 should not be intercepted
	_, handled = pm.HandleMemoryRead(0x4000)
	if handled {
		t.Error("memory read at 0x4000 should not be intercepted")
	}
}

func TestHandleMemoryRead_DiscipleROMPaged(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)

	disc := pm.GetDisciple()
	// Page in via port 0xBB read (patch page)
	disc.HandlePortRead(0xBB)
	if !disc.IsROMPaged() {
		t.Fatal("Disciple ROM should be paged in")
	}

	val, handled := pm.HandleMemoryRead(0x0000)
	if !handled {
		t.Error("memory read at 0x0000 should be intercepted when Disciple ROM is paged in")
	}
	expectedROM := disc.GetROM()
	if val != expectedROM[0] {
		t.Errorf("memory read value = 0x%02X, want 0x%02X", val, expectedROM[0])
	}
}

// --- HandleMemoryWrite ---

func TestHandleMemoryWrite_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	handled := pm.HandleMemoryWrite(0x2000, 0xAA)
	if handled {
		t.Error("memory write should not be intercepted with no peripherals")
	}
}

func TestHandleMemoryWrite_MultifaceRAM(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)
	pm.HandleNMI() // Page in ROM

	// Writing to 0x2000-0x3FFF should go to Multiface RAM
	handled := pm.HandleMemoryWrite(0x2000, 0xBE)
	if !handled {
		t.Error("memory write at 0x2000 should be intercepted when Multiface ROM is paged in")
	}
	mfRAM := pm.GetMultiface().GetRAM()
	if mfRAM[0] != 0xBE {
		t.Errorf("Multiface RAM[0] = 0x%02X, want 0xBE", mfRAM[0])
	}
}

// --- HandleOpcodeRead ---

func TestHandleOpcodeRead_NoPeripherals(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	if pm.HandleOpcodeRead(0x0066) {
		t.Error("opcode read should not be handled with no peripherals")
	}
}

func TestHandleOpcodeRead_MultifaceAtNMIVector(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)
	// Press the red button first
	pm.HandleNMI()

	// Opcode read at NMI vector should trigger ROM paging
	handled := pm.HandleOpcodeRead(0x0066)
	if !handled {
		t.Error("opcode read at NMI vector should be handled")
	}
}

// --- SetDiscipleInhibit ---

func TestSetDiscipleInhibit(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableDisciple(dir)

	pm.SetDiscipleInhibit(true)
	if !pm.GetDisciple().IsInhibited() {
		t.Error("Disciple should be inhibited")
	}

	pm.SetDiscipleInhibit(false)
	if pm.GetDisciple().IsInhibited() {
		t.Error("Disciple should not be inhibited")
	}
}

func TestSetDiscipleInhibit_WhenDisabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	// Should not panic when Disciple is not enabled
	pm.SetDiscipleInhibit(true)
}

// --- SetMultifaceEnabled ---

func TestSetMultifaceEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	_ = pm.EnableMultiface(multiface.Multiface1, dir)

	pm.SetMultifaceEnabled(false)
	if pm.GetMultiface().IsEnabled() {
		t.Error("Multiface should be disabled via SetMultifaceEnabled(false)")
	}

	pm.SetMultifaceEnabled(true)
	if !pm.GetMultiface().IsEnabled() {
		t.Error("Multiface should be re-enabled via SetMultifaceEnabled(true)")
	}
}

func TestSetMultifaceEnabled_WhenDisabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	// Should not panic when Multiface is not enabled
	pm.SetMultifaceEnabled(false)
}

// --- LoadDiscipleDisk ---

func TestLoadDiscipleDisk_NotEnabled(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)

	err := pm.LoadDiscipleDisk(0, "test.mgt")
	if err == nil {
		t.Error("LoadDiscipleDisk should fail when Disciple is not enabled")
	}
}

// PeripheralManager IF1 + IF2 + Plus3 method coverage.

func TestEnableInterface1_Only48K(t *testing.T) {
	dir := createTestROMDir(t)

	// 48K mem → IF1 attach allowed.
	mem48 := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem48, dir)
	rom := make([]byte, 8192)
	if err := pm.EnableInterface1(rom); err != nil {
		t.Errorf("EnableInterface1 on 48K = %v, want nil", err)
	}
	// Re-enable is a no-op.
	if err := pm.EnableInterface1(rom); err != nil {
		t.Errorf("EnableInterface1 second call: %v", err)
	}
	pm.DisableInterface1()

	// 128K mem → refused.
	dir128 := t.TempDir()
	createTestROMs128(t, dir128)
	mem128, err := memory.New(dir128, roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	pm2 := NewPeripheralManager(mem128, dir128)
	if err := pm2.EnableInterface1(rom); err == nil {
		t.Errorf("EnableInterface1 on 128K = nil, want error")
	}
}

func TestEnableInterface1_BadROMSize(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	if err := pm.EnableInterface1([]byte{0x00}); err == nil {
		t.Errorf("EnableInterface1 with 1-byte ROM = nil, want error")
	}
}

func TestInsertInterface2Cartridge_Only48K(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir) // 48K
	pm := NewPeripheralManager(mem, dir)
	// IF2 cart loading needs a real 16KB .rom file.
	cartPath := filepath.Join(dir, "test.rom")
	cartBytes := make([]byte, 16384)
	if err := os.WriteFile(cartPath, cartBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pm.InsertInterface2Cartridge(cartPath); err != nil {
		t.Errorf("InsertInterface2Cartridge on 48K = %v", err)
	}
	if !pm.IsInterface2CartridgeInserted() {
		t.Errorf("after Insert: IsInterface2CartridgeInserted = false")
	}
	pm.RemoveInterface2Cartridge()
	if pm.IsInterface2CartridgeInserted() {
		t.Errorf("after Remove: IsInterface2CartridgeInserted = true")
	}
	// Double-remove is a no-op.
	pm.RemoveInterface2Cartridge()
}

func TestInsertInterface2Cartridge_RefuseNon48K(t *testing.T) {
	dir128 := t.TempDir()
	createTestROMs128(t, dir128)
	mem, err := memory.New(dir128, roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	pm := NewPeripheralManager(mem, dir128)
	cartPath := filepath.Join(dir128, "x.rom")
	_ = os.WriteFile(cartPath, make([]byte, 16384), 0o644)
	if err := pm.InsertInterface2Cartridge(cartPath); err == nil {
		t.Errorf("Insert on 128K = nil err, want refusal")
	}
}

func TestPlus3DiskEjectAndProtect_NoOpsOnEmpty(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	pm := NewPeripheralManager(mem, dir)
	// Eject on empty drive — must not panic.
	pm.EjectPlus3Disk(0)
	pm.SetPlus3WriteProtect(0, true)
	pm.SetPlus3Speedlock(true)
	pm.SetPlus3Speedlock(false)
}

// createTestROMs128 sets up 48.rom + 128-0/1.rom for 128K models.
func createTestROMs128(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		data := make([]byte, 16384)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
