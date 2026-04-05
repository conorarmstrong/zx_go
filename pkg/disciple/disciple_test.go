package disciple

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// createTestROMDir builds a temporary directory with the minimal ROM files
// required by memory.New and the Disciple interface.
func createTestROMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// 48K system ROM (16KB) -- required by memory.New
	sysROM := make([]byte, 16384)
	for i := range sysROM {
		sysROM[i] = byte(i & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatalf("write 48.rom: %v", err)
	}

	// GDOS ROM for Disciple (8KB)
	gdosROM := make([]byte, 8192)
	for i := range gdosROM {
		gdosROM[i] = byte((i + 0x42) & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, "gdos.rom"), gdosROM, 0644); err != nil {
		t.Fatalf("write gdos.rom: %v", err)
	}

	return dir
}

// newTestMemory creates a *memory.Memory backed by the test ROM directory.
func newTestMemory(t *testing.T, dir string) *memory.Memory {
	t.Helper()
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New failed: %v", err)
	}
	return mem
}

// --- Creation ---

func TestNewDisciple_WithRealROM(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)

	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple with real ROM failed: %v", err)
	}

	if !d.IsEnabled() {
		t.Error("newly created Disciple should be enabled")
	}
	if d.IsInhibited() {
		t.Error("newly created Disciple should not be inhibited")
	}
	if d.IsROMPaged() {
		t.Error("newly created Disciple should not have ROM paged in")
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("ROM size = %d, want 8192", len(d.GetROM()))
	}
	if len(d.GetRAM()) != 0x2000 {
		t.Errorf("RAM size = %d, want 8192", len(d.GetRAM()))
	}

	// Verify the ROM contents match what we wrote
	if d.GetROM()[0] != byte(0x42&0xFF) {
		t.Errorf("ROM[0] = 0x%02X, want 0x42", d.GetROM()[0])
	}
}

func TestNewDisciple_WithoutRealROM(t *testing.T) {
	// Use an empty directory -- Disciple falls back to a placeholder ROM
	dir := t.TempDir()

	// We still need system ROMs for memory.New; create just 48.rom
	sysROM := make([]byte, 16384)
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatal(err)
	}
	mem := newTestMemory(t, dir)

	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple with placeholder ROM should not fail: %v", err)
	}

	if len(d.GetROM()) != 0x2000 {
		t.Errorf("placeholder ROM size = %d, want 8192", len(d.GetROM()))
	}
	// Placeholder starts with DI (0xF3)
	if d.GetROM()[0] != 0xF3 {
		t.Errorf("placeholder ROM[0] = 0x%02X, want 0xF3", d.GetROM()[0])
	}
}

// --- Port reads ---

func TestHandlePortRead_AllPorts(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	tests := []struct {
		name string
		port uint16
	}{
		{"Control", PortControl},
		{"Status", PortStatus},
		{"Track", PortTrack},
		{"Sector", PortSector},
		{"Data", PortData},
		{"Drive", PortDrive},
		{"System", PortSystem},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, handled := d.HandlePortRead(tc.port)
			if !handled {
				t.Errorf("port 0x%02X should be handled", tc.port)
			}
		})
	}
}

func TestHandlePortRead_UnknownPort(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	_, handled := d.HandlePortRead(0xFF)
	if handled {
		t.Error("unknown port 0xFF should not be handled")
	}
}

func TestHandlePortRead_WhenInhibited(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.SetInhibit(true)

	_, handled := d.HandlePortRead(PortControl)
	if handled {
		t.Error("port reads should not be handled when inhibited")
	}
}

func TestHandlePortRead_StatusRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Without a disk loaded, status should have "not ready" (bit 0) set
	val, _ := d.HandlePortRead(PortStatus)
	if val&0x01 == 0 {
		t.Error("status bit 0 (not ready) should be set without a disk")
	}
	// Data request (bit 6) should be set
	if val&0x40 == 0 {
		t.Error("status bit 6 (data request) should be set")
	}
}

// --- Port writes ---

func TestHandlePortWrite_TrackRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	handled := d.HandlePortWrite(PortTrack, 0x2A)
	if !handled {
		t.Error("track port write should be handled")
	}

	val, _ := d.HandlePortRead(PortTrack)
	if val != 0x2A {
		t.Errorf("track = 0x%02X, want 0x2A", val)
	}
}

func TestHandlePortWrite_SectorRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortSector, 0x05)

	val, _ := d.HandlePortRead(PortSector)
	if val != 0x05 {
		t.Errorf("sector = 0x%02X, want 0x05", val)
	}
}

func TestHandlePortWrite_DataRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortData, 0xBB)

	val, _ := d.HandlePortRead(PortData)
	if val != 0xBB {
		t.Errorf("data = 0x%02X, want 0xBB", val)
	}
}

func TestHandlePortWrite_WhenInhibited(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.SetInhibit(true)

	handled := d.HandlePortWrite(PortTrack, 0x10)
	if handled {
		t.Error("port writes should not be handled when inhibited")
	}
}

func TestHandlePortWrite_UnknownPort(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	handled := d.HandlePortWrite(0xFF, 0x00)
	if handled {
		t.Error("unknown port 0xFF should not be handled")
	}
}

// --- Drive selection ---

func TestDriveSelection(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Select drive 1
	d.HandlePortWrite(PortDrive, 0x01)
	val, _ := d.HandlePortRead(PortDrive)
	if val != 0x01 {
		t.Errorf("drive = %d, want 1", val)
	}

	// Select drive 2 -- should be masked to 2 bits (0-3)
	d.HandlePortWrite(PortDrive, 0x02)
	val, _ = d.HandlePortRead(PortDrive)
	if val != 0x02 {
		t.Errorf("drive = %d, want 2", val)
	}

	// High bits should be masked off
	d.HandlePortWrite(PortDrive, 0xFF)
	val, _ = d.HandlePortRead(PortDrive)
	if val != 0x03 {
		t.Errorf("drive = %d after 0xFF, want 3 (masked)", val)
	}
}

// --- Disk controller commands ---

func TestCommand_Restore(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Move to a non-zero track first
	d.HandlePortWrite(PortTrack, 0x20)
	// Issue Restore command
	d.HandlePortWrite(PortControl, CmdRestore)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 0 {
		t.Errorf("track after Restore = %d, want 0", track)
	}
}

func TestCommand_Seek(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Set data register to target track
	d.HandlePortWrite(PortData, 0x15)
	// Issue Seek command
	d.HandlePortWrite(PortControl, CmdSeek)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 0x15 {
		t.Errorf("track after Seek = 0x%02X, want 0x15", track)
	}
}

func TestCommand_StepIn(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Start at track 0
	d.HandlePortWrite(PortControl, CmdRestore)
	// Step in
	d.HandlePortWrite(PortControl, CmdStepIn)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 1 {
		t.Errorf("track after StepIn from 0 = %d, want 1", track)
	}
}

func TestCommand_StepIn_MaxTrack(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Set track to 79 (max)
	d.HandlePortWrite(PortTrack, 79)
	// Step in -- should stay at 79
	d.HandlePortWrite(PortControl, CmdStepIn)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 79 {
		t.Errorf("track after StepIn at max = %d, want 79", track)
	}
}

func TestCommand_StepOut(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortTrack, 5)
	d.HandlePortWrite(PortControl, CmdStepOut)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 4 {
		t.Errorf("track after StepOut from 5 = %d, want 4", track)
	}
}

func TestCommand_StepOut_MinTrack(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortControl, CmdRestore) // track = 0
	d.HandlePortWrite(PortControl, CmdStepOut)

	track, _ := d.HandlePortRead(PortTrack)
	if track != 0 {
		t.Errorf("track after StepOut at min = %d, want 0", track)
	}
}

func TestCommand_ForceInterrupt(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortControl, CmdForceInt)
	// After ForceInt the controller should report not-busy (status = 0x80)
	// We can verify by reading the control register (last command)
	ctrl, _ := d.HandlePortRead(PortControl)
	if ctrl != CmdForceInt {
		t.Errorf("control register = 0x%02X, want 0x%02X", ctrl, CmdForceInt)
	}
}

func TestCommand_ReadSector(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(PortControl, CmdReadSector)
	// The placeholder implementation returns data = 0x00
	data, _ := d.HandlePortRead(PortData)
	if data != 0x00 {
		t.Errorf("data after ReadSector = 0x%02X, want 0x00", data)
	}
}

func TestCommand_WriteSector(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Just verify it does not panic
	d.HandlePortWrite(PortControl, CmdWriteSector)
}

// --- ROM paging ---

func TestROMPaging_NMIControl(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if d.IsROMPaged() {
		t.Error("ROM should not be paged in initially")
	}

	// NMI with bit 0 set pages in ROM
	d.HandlePortWrite(PortNMI, 0x01)
	if !d.IsROMPaged() {
		t.Error("ROM should be paged in after NMI with bit 0 set")
	}

	// NMI with bit 0 clear pages out ROM
	d.HandlePortWrite(PortNMI, 0x00)
	if d.IsROMPaged() {
		t.Error("ROM should be paged out after NMI with bit 0 clear")
	}
}

func TestROMPaging_SystemControl(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// System control bit 0 = ROM page in
	d.HandlePortWrite(PortSystem, 0x01)
	if !d.IsROMPaged() {
		t.Error("ROM should be paged in via system control")
	}

	// System control bit 1 = inhibit (also pages out ROM)
	d.HandlePortWrite(PortSystem, 0x02)
	if d.IsROMPaged() {
		t.Error("ROM should be paged out when inhibited")
	}
	if !d.IsInhibited() {
		t.Error("Disciple should be inhibited after system control bit 1")
	}
}

func TestROMPaging_InhibitPreventsPageIn(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.SetInhibit(true)

	// Try to page in via NMI -- should be ignored because inhibited
	// (port writes are blocked when inhibited)
	handled := d.HandlePortWrite(PortNMI, 0x01)
	if handled {
		t.Error("port write should not be handled when inhibited")
	}
	if d.IsROMPaged() {
		t.Error("ROM should not be paged in when inhibited")
	}
}

// --- SetInhibit ---

func TestSetInhibit(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Page in ROM first
	d.HandlePortWrite(PortNMI, 0x01)
	if !d.IsROMPaged() {
		t.Fatal("ROM should be paged in")
	}

	// Inhibit should also page out ROM
	d.SetInhibit(true)
	if !d.IsInhibited() {
		t.Error("should be inhibited")
	}
	if d.IsROMPaged() {
		t.Error("ROM should be paged out after inhibit")
	}
	if d.IsEnabled() {
		t.Error("IsEnabled should be false when inhibited")
	}

	// Uninhibit
	d.SetInhibit(false)
	if d.IsInhibited() {
		t.Error("should not be inhibited after SetInhibit(false)")
	}
	if !d.IsEnabled() {
		t.Error("IsEnabled should be true after uninhibit")
	}
}

// --- LoadDisk ---

func TestLoadDisk_InvalidDrive(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if err := d.LoadDisk(-1, "test.mgt"); err == nil {
		t.Error("LoadDisk with drive -1 should fail")
	}
	if err := d.LoadDisk(2, "test.mgt"); err == nil {
		t.Error("LoadDisk with drive 2 should fail")
	}
}

func TestLoadDisk_FileNotFound(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	err := d.LoadDisk(0, "/nonexistent/disk.mgt")
	if err == nil {
		t.Error("LoadDisk with nonexistent file should fail")
	}
}

func TestLoadDisk_ValidFile(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Create a dummy disk image
	diskPath := filepath.Join(dir, "test.mgt")
	if err := os.WriteFile(diskPath, make([]byte, 819200), 0644); err != nil {
		t.Fatal(err)
	}

	if err := d.LoadDisk(0, diskPath); err != nil {
		t.Fatalf("LoadDisk failed: %v", err)
	}

	// After loading a disk, status should show ready (bit 0 clear) and head loaded (bit 2 set)
	status, _ := d.HandlePortRead(PortStatus)
	if status&0x01 != 0 {
		t.Error("status bit 0 (not ready) should be clear after disk load")
	}
	if status&0x04 == 0 {
		t.Error("status bit 2 (head loaded) should be set after disk load")
	}
}

// --- GetROM / GetRAM ---

func TestGetROM_ReturnsCorrectData(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	rom := d.GetROM()
	if len(rom) != 0x2000 {
		t.Errorf("ROM length = %d, want 8192", len(rom))
	}
}

func TestGetRAM_IsWritable(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	ram := d.GetRAM()
	ram[0] = 0xAB
	// Verify we got the same backing array
	if d.GetRAM()[0] != 0xAB {
		t.Error("GetRAM should return the same backing slice")
	}
}

// --- System status ---

func TestSystemStatus(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	// Initial system status
	sys, _ := d.HandlePortRead(PortSystem)
	if sys&0x01 != 0 {
		t.Error("system status bit 0 (ROM paged) should be clear initially")
	}
	if sys&0x02 != 0 {
		t.Error("system status bit 1 (NMI pressed) should be clear initially")
	}

	// Page in ROM and press NMI
	d.HandlePortWrite(PortNMI, 0x01)

	sys, _ = d.HandlePortRead(PortSystem)
	if sys&0x01 == 0 {
		t.Error("system status bit 0 should be set when ROM is paged in")
	}
	if sys&0x02 == 0 {
		t.Error("system status bit 1 should be set when NMI is pressed")
	}
}
