package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Helper to create test ROM files
func createTestROMs(t *testing.T, dir string) {
	// Create test directory
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Create dummy ROM files
	roms := map[string][]byte{
		"48.rom":    make([]byte, PageSize),
		"128-0.rom": make([]byte, PageSize),
		"128-1.rom": make([]byte, PageSize),
	}

	// Fill ROMs with test patterns
	for i := 0; i < PageSize; i++ {
		roms["48.rom"][i] = byte(i & 0xFF)
		roms["128-0.rom"][i] = byte((i + 0x10) & 0xFF)
		roms["128-1.rom"][i] = byte((i + 0x20) & 0xFF)
	}

	// Write ROM files
	for name, data := range roms {
		err := os.WriteFile(filepath.Join(dir, name), data, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Clean up test ROMs
func cleanupTestROMs(dir string) {
	_ = os.RemoveAll(dir)
}

func TestMemoryCreation(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	// Test 48K memory creation
	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create 48K memory: %v", err)
	}

	if mem.PagingEnabled {
		t.Errorf("48K memory should not have paging enabled")
	}

	if mem.ScreenPage != 5 {
		t.Errorf("48K screen page should be 5, got %d", mem.ScreenPage)
	}

	// Test 128K memory creation
	mem, err = New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create 128K memory: %v", err)
	}

	if !mem.PagingEnabled {
		t.Errorf("128K memory should have paging enabled")
	}
}

func TestMemoryMapping48K(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Test ROM mapping (0x0000-0x3FFF)
	val := mem.Read(0x0000)
	expected := byte(0x00) // First byte of 48.rom test pattern
	if val != expected {
		t.Errorf("ROM read at 0x0000: expected 0x%02X, got 0x%02X", expected, val)
	}

	val = mem.Read(0x0100)
	expected = byte(0x00) // 0x0100 & 0xFF = 0x00
	if val != expected {
		t.Errorf("ROM read at 0x0100: expected 0x%02X, got 0x%02X", expected, val)
	}

	// Test RAM mapping (0x4000-0xFFFF)
	// Write to RAM
	mem.Write(0x4000, 0x42)
	val = mem.Read(0x4000)
	if val != 0x42 {
		t.Errorf("RAM write/read at 0x4000: expected 0x42, got 0x%02X", val)
	}

	// Test ROM write protection
	mem.Write(0x0000, 0x99)
	val = mem.Read(0x0000)
	if val == 0x99 {
		t.Errorf("ROM should be write protected")
	}
}

func TestMemoryMapping128K(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Test ROM mapping (0x0000-0x3FFF) - should be 128-0.rom
	val := mem.Read(0x0000)
	expected := byte(0x10) // First byte of 128-0.rom test pattern
	if val != expected {
		t.Errorf("128K ROM read at 0x0000: expected 0x%02X, got 0x%02X", expected, val)
	}

	// Test default RAM mapping
	mem.Write(0x4000, 0x55) // RAM page 5
	val = mem.Read(0x4000)
	if val != 0x55 {
		t.Errorf("128K RAM write/read at 0x4000: expected 0x55, got 0x%02X", val)
	}
}

func TestMemoryPaging128K(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Write different values to different RAM pages at the same address
	// First, switch to page 0
	mem.PageMemory(0x00) // Page 0 to 0xC000
	mem.Write(0xC000, 0x11)

	// Switch to page 1
	mem.PageMemory(0x01) // Page 1 to 0xC000
	mem.Write(0xC000, 0x22)

	// Switch to page 2
	mem.PageMemory(0x02) // Page 2 to 0xC000
	mem.Write(0xC000, 0x33)

	// Verify each page has its own data
	mem.PageMemory(0x00)
	if mem.Read(0xC000) != 0x11 {
		t.Errorf("Page 0: expected 0x11, got 0x%02X", mem.Read(0xC000))
	}

	mem.PageMemory(0x01)
	if mem.Read(0xC000) != 0x22 {
		t.Errorf("Page 1: expected 0x22, got 0x%02X", mem.Read(0xC000))
	}

	mem.PageMemory(0x02)
	if mem.Read(0xC000) != 0x33 {
		t.Errorf("Page 2: expected 0x33, got 0x%02X", mem.Read(0xC000))
	}
}

func TestROMPaging128K(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Default should be ROM 0 (128-0.rom)
	val := mem.Read(0x0000)
	expected := byte(0x10) // 128-0.rom pattern
	if val != expected {
		t.Errorf("ROM 0: expected 0x%02X, got 0x%02X", expected, val)
	}

	// Switch to ROM 1 (128-1.rom)
	mem.PageMemory(0x10) // Bit 4 set = ROM 1
	val = mem.Read(0x0000)
	expected = byte(0x20) // 128-1.rom pattern
	if val != expected {
		t.Errorf("ROM 1: expected 0x%02X, got 0x%02X", expected, val)
	}

	// Switch back to ROM 0
	mem.PageMemory(0x00) // Bit 4 clear = ROM 0
	val = mem.Read(0x0000)
	expected = byte(0x10) // 128-0.rom pattern
	if val != expected {
		t.Errorf("ROM 0 again: expected 0x%02X, got 0x%02X", expected, val)
	}
}

func TestScreenPageSwitching(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Default screen page should be 5
	if mem.ScreenPage != 5 {
		t.Errorf("Default screen page should be 5, got %d", mem.ScreenPage)
	}

	// Switch to screen page 7
	mem.PageMemory(0x08) // Bit 3 set = screen page 7
	if mem.ScreenPage != 7 {
		t.Errorf("Screen page should be 7, got %d", mem.ScreenPage)
	}

	// Switch back to screen page 5
	mem.PageMemory(0x00) // Bit 3 clear = screen page 5
	if mem.ScreenPage != 5 {
		t.Errorf("Screen page should be 5, got %d", mem.ScreenPage)
	}
}

func TestPagingDisable(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Initially paging should be enabled
	if !mem.PagingEnabled {
		t.Errorf("Paging should be enabled initially")
	}

	// Disable paging
	mem.PageMemory(0x20) // Bit 5 set = disable paging
	if mem.PagingEnabled {
		t.Errorf("Paging should be disabled")
	}

	// Try to page memory - should be ignored
	oldScreenPage := mem.ScreenPage
	mem.PageMemory(0x08) // Try to switch screen page
	if mem.ScreenPage != oldScreenPage {
		t.Errorf("Memory paging should be ignored when disabled")
	}
}

func TestMemoryConstants(t *testing.T) {
	if PageSize != 0x4000 {
		t.Errorf("PageSize should be 0x4000, got 0x%X", PageSize)
	}

	if RAMSize48K != 0xC000 {
		t.Errorf("RAMSize48K should be 0xC000, got 0x%X", RAMSize48K)
	}

	if RAMSize128K != 0x20000 {
		t.Errorf("RAMSize128K should be 0x20000, got 0x%X", RAMSize128K)
	}
}

func TestGetPage(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Test getting a page directly
	page := mem.GetPage(0)
	if len(page) != PageSize {
		t.Errorf("Page size should be %d, got %d", PageSize, len(page))
	}

	// Write to page directly and verify via normal memory access
	page[0] = 0x42
	mem.memoryPageReadMap[3] = 0 // Map page 0 to 0xC000-0xFFFF
	mem.memoryPageWriteMap[3] = 0

	val := mem.Read(0xC000)
	if val != 0x42 {
		t.Errorf("Direct page write should be visible via memory read: expected 0x42, got 0x%02X", val)
	}
}

func TestMemoryBoundaries(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Test all 64KB addresses
	for addr := 0x0000; addr <= 0xFFFF; addr += 0x1000 {
		// ROM area (0x0000-0x3FFF) should not be writable
		if addr < 0x4000 {
			originalVal := mem.Read(uint16(addr))
			mem.Write(uint16(addr), 0x99)
			newVal := mem.Read(uint16(addr))
			if originalVal != newVal {
				t.Errorf("ROM at 0x%04X should be write-protected", addr)
			}
		} else {
			// RAM area should be writable
			mem.Write(uint16(addr), 0xAA)
			val := mem.Read(uint16(addr))
			if val != 0xAA {
				t.Errorf("RAM at 0x%04X should be writable: expected 0xAA, got 0x%02X", addr, val)
			}
		}
	}
}

// Benchmark memory operations
func BenchmarkMemoryRead(b *testing.B) {
	testDir := "test_roms"
	createTestROMs(&testing.T{}, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := New(testDir, roms.Model48K)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.Read(0x4000)
	}
}

func BenchmarkMemoryWrite(b *testing.B) {
	testDir := "test_roms"
	createTestROMs(&testing.T{}, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := New(testDir, roms.Model48K)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.Write(0x4000, 0x42)
	}
}

func TestContendedMemory(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Set up contention
	var tstates uint64
	mem.TStates = &tstates
	mem.ContentionEnabled = true

	// Access contended address during active display (tstate 14335-57343)
	tstates = 14335
	before := tstates
	mem.ContendMemory(0x4000) // Contended address
	if tstates <= before {
		t.Error("Contended memory access during active display should add delay")
	}

	// Access non-contended address — should not add delay
	tstates = 14335
	before = tstates
	mem.ContendMemory(0x8000) // Non-contended address
	if tstates != before {
		t.Errorf("Non-contended address should not add delay")
	}

	// Access outside active display — should not add delay
	tstates = 0
	before = tstates
	mem.ContendMemory(0x4000)
	if tstates != before {
		t.Errorf("Access outside active display should not add delay")
	}
}

func TestPlus3SpecialPaging(t *testing.T) {
	testDir := "test_roms"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	// Need +3 ROMs — create them
	romFiles := map[string][]byte{
		"plus3-0.rom": make([]byte, PageSize),
		"plus3-1.rom": make([]byte, PageSize),
		"plus3-2.rom": make([]byte, PageSize),
		"plus3-3.rom": make([]byte, PageSize),
	}
	for name, data := range romFiles {
		if err := os.WriteFile(filepath.Join(testDir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	mem, err := New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("Failed to create +3 memory: %v", err)
	}

	// Write distinct values to different RAM banks
	for bank := 0; bank < 8; bank++ {
		page := mem.GetPage(bank)
		page[0] = byte(0x10 + bank)
	}

	// Enable special paging mode 0: banks 0,1,2,3
	mem.PageMemoryPlus3(0x01) // bit 0 set = special mode, mode 0

	// Read from 0xC000 should be bank 3
	val := mem.Read(0xC000)
	if val != 0x13 { // 0x10 + 3
		t.Errorf("Special mode 0, slot 3: expected 0x13, got 0x%02X", val)
	}

	// Read from 0x0000 should be bank 0 (RAM, writable)
	val = mem.Read(0x0000)
	if val != 0x10 { // 0x10 + 0
		t.Errorf("Special mode 0, slot 0: expected 0x10, got 0x%02X", val)
	}

	// Disable special paging
	mem.PageMemoryPlus3(0x00) // bit 0 clear = normal mode
}

// TestReset checks that Reset restores the model's power-on paging
// state — port 7FFD and 1FFD zeroed, special paging cleared, page map
// back to defaults. This is what unblocks Emulator/Reboot after a +3
// disk has paged a non-default ROM in: without Reset the boot ROM
// index stays whatever the game last selected and the +3 main menu
// never reappears.
func TestReset(t *testing.T) {
	testDir := "test_roms_reset"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	for _, name := range []string{"plus3-0.rom", "plus3-1.rom", "plus3-2.rom", "plus3-3.rom"} {
		if err := os.WriteFile(filepath.Join(testDir, name), make([]byte, PageSize), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mem, err := New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("New +3: %v", err)
	}

	// Simulate a game leaving the paging in a non-default state, like
	// Loopz +3 does after its loader runs: 7FFD=0x10 (ROM-low bit set),
	// 1FFD=0x0C (ROM-high bit set) — combined ROM index 3 (BASIC),
	// which is NOT the +3 boot-menu ROM.
	mem.PageMemory(0x10)
	mem.PageMemoryPlus3(0x0C)
	p7, p1, special := mem.GetPortState()
	if p7 != 0x10 || p1 != 0x0C || special {
		t.Fatalf("setup failed: 7FFD=%02X 1FFD=%02X special=%v", p7, p1, special)
	}
	readMap, _ := mem.GetPageMap()
	if readMap[0] == 16 {
		t.Fatalf("setup failed: still ROM 0 at slot 0 (readMap[0]=%d)", readMap[0])
	}

	if err := mem.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	p7, p1, special = mem.GetPortState()
	if p7 != 0 || p1 != 0 || special {
		t.Errorf("after Reset: 7FFD=%02X 1FFD=%02X special=%v, want 0/0/false", p7, p1, special)
	}
	readMap, _ = mem.GetPageMap()
	if readMap[0] != 16 {
		t.Errorf("after Reset: slot 0 = %d, want 16 (ROM 0 = +3 boot menu)", readMap[0])
	}
}

func BenchmarkMemoryPaging(b *testing.B) {
	testDir := "test_roms"
	createTestROMs(&testing.T{}, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := New(testDir, roms.Model128K)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.PageMemory(byte(i & 7))
	}
}

// =============================================================================
// TestSwitchModel — switch from 48K to 128K and back
// =============================================================================

func TestSwitchModel(t *testing.T) {
	testDir := "test_roms_switch"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create 48K memory: %v", err)
	}

	if mem.GetCurrentModel() != roms.Model48K {
		t.Errorf("Initial model should be 48K")
	}
	if mem.PagingEnabled {
		t.Errorf("48K should not have paging enabled")
	}

	// Switch to 128K
	err = mem.SwitchModel(roms.Model128K)
	if err != nil {
		t.Fatalf("SwitchModel to 128K failed: %v", err)
	}
	if mem.GetCurrentModel() != roms.Model128K {
		t.Errorf("After switch, model should be 128K")
	}
	if !mem.PagingEnabled {
		t.Errorf("128K should have paging enabled")
	}

	// Switch back to 48K
	err = mem.SwitchModel(roms.Model48K)
	if err != nil {
		t.Fatalf("SwitchModel back to 48K failed: %v", err)
	}
	if mem.GetCurrentModel() != roms.Model48K {
		t.Errorf("After switch back, model should be 48K")
	}
	if mem.PagingEnabled {
		t.Errorf("48K should not have paging enabled after switch back")
	}
}

// =============================================================================
// TestGetCurrentModel / TestGetROMManager
// =============================================================================

func TestGetCurrentModel(t *testing.T) {
	testDir := "test_roms_model"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem48, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create 48K memory: %v", err)
	}
	if mem48.GetCurrentModel() != roms.Model48K {
		t.Errorf("GetCurrentModel: expected Model48K, got %v", mem48.GetCurrentModel())
	}

	mem128, err := New(testDir, roms.Model128K)
	if err != nil {
		t.Fatalf("Failed to create 128K memory: %v", err)
	}
	if mem128.GetCurrentModel() != roms.Model128K {
		t.Errorf("GetCurrentModel: expected Model128K, got %v", mem128.GetCurrentModel())
	}
}

func TestGetROMManager(t *testing.T) {
	testDir := "test_roms_romman"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	rm := mem.GetROMManager()
	if rm == nil {
		t.Errorf("GetROMManager: expected non-nil ROM manager")
	}

	// The 48K ROM should be loaded
	rom, exists := rm.GetROM(roms.ROM48K)
	if !exists {
		t.Errorf("GetROMManager: 48K ROM should be loaded")
	}
	if len(rom) != PageSize {
		t.Errorf("GetROMManager: ROM size should be %d, got %d", PageSize, len(rom))
	}
}

// =============================================================================
// TestNewLegacy — compatibility constructor
// =============================================================================

func TestNewLegacy(t *testing.T) {
	testDir := "test_roms_legacy"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	// 48K via legacy interface
	mem48, err := NewLegacy(testDir, false)
	if err != nil {
		t.Fatalf("NewLegacy(48K) failed: %v", err)
	}
	if mem48.GetCurrentModel() != roms.Model48K {
		t.Errorf("NewLegacy(false): expected Model48K, got %v", mem48.GetCurrentModel())
	}
	if mem48.PagingEnabled {
		t.Errorf("NewLegacy(false): 48K should not have paging enabled")
	}

	// 128K via legacy interface
	mem128, err := NewLegacy(testDir, true)
	if err != nil {
		t.Fatalf("NewLegacy(128K) failed: %v", err)
	}
	if mem128.GetCurrentModel() != roms.Model128K {
		t.Errorf("NewLegacy(true): expected Model128K, got %v", mem128.GetCurrentModel())
	}
	if !mem128.PagingEnabled {
		t.Errorf("NewLegacy(true): 128K should have paging enabled")
	}
}

// Helper to create plus2 and plus3 ROM files in addition to base ROMs
func createTestROMsPlus(t *testing.T, dir string) {
	t.Helper()
	createTestROMs(t, dir)

	extra := map[string][]byte{
		"plus2-0.rom": make([]byte, PageSize),
		"plus2-1.rom": make([]byte, PageSize),
		"plus3-0.rom": make([]byte, PageSize),
		"plus3-1.rom": make([]byte, PageSize),
		"plus3-2.rom": make([]byte, PageSize),
		"plus3-3.rom": make([]byte, PageSize),
	}
	for i := 0; i < PageSize; i++ {
		extra["plus2-0.rom"][i] = byte((i + 0x30) & 0xFF)
		extra["plus2-1.rom"][i] = byte((i + 0x40) & 0xFF)
		extra["plus3-0.rom"][i] = byte((i + 0x50) & 0xFF)
		extra["plus3-1.rom"][i] = byte((i + 0x60) & 0xFF)
		extra["plus3-2.rom"][i] = byte((i + 0x70) & 0xFF)
		extra["plus3-3.rom"][i] = byte((i + 0x80) & 0xFF)
	}
	for name, data := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// =============================================================================
// TestPlus2Model, TestPlus2AModel
// =============================================================================

func TestPlus2Model(t *testing.T) {
	testDir := "test_roms_plus2"
	createTestROMsPlus(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.ModelPlus2)
	if err != nil {
		t.Fatalf("Failed to create +2 memory: %v", err)
	}

	if mem.GetCurrentModel() != roms.ModelPlus2 {
		t.Errorf("TestPlus2Model: expected ModelPlus2, got %v", mem.GetCurrentModel())
	}
	if !mem.PagingEnabled {
		t.Errorf("+2 model should have paging enabled")
	}
	if mem.ScreenPage != 5 {
		t.Errorf("+2 model default screen page should be 5, got %d", mem.ScreenPage)
	}

	// Verify first byte of ROM is the +2 ROM pattern (0x30)
	val := mem.Read(0x0000)
	if val != 0x30 {
		t.Errorf("+2 ROM first byte: expected 0x30, got 0x%02X", val)
	}
}

func TestPlus2AModel(t *testing.T) {
	testDir := "test_roms_plus2a"
	createTestROMsPlus(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.ModelPlus2A)
	if err != nil {
		t.Fatalf("Failed to create +2A memory: %v", err)
	}

	if mem.GetCurrentModel() != roms.ModelPlus2A {
		t.Errorf("TestPlus2AModel: expected ModelPlus2A, got %v", mem.GetCurrentModel())
	}
	if !mem.PagingEnabled {
		t.Errorf("+2A model should have paging enabled")
	}
	if mem.ScreenPage != 5 {
		t.Errorf("+2A model default screen page should be 5, got %d", mem.ScreenPage)
	}

	// +2A uses plus3-0.rom, which has pattern 0x50
	val := mem.Read(0x0000)
	if val != 0x50 {
		t.Errorf("+2A ROM first byte: expected 0x50, got 0x%02X", val)
	}
}

// =============================================================================
// TestPageMemoryPlus3_AllModes — all 4 special paging modes
// =============================================================================

func TestPageMemoryPlus3_AllModes(t *testing.T) {
	testDir := "test_roms_plus3modes"
	createTestROMsPlus(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("Failed to create +3 memory: %v", err)
	}

	// Write a distinct marker byte to each RAM bank at offset 0
	for bank := 0; bank < 8; bank++ {
		page := mem.GetPage(bank)
		page[0] = byte(0x10 + bank)
	}

	tests := []struct {
		val       byte // port 0x1FFD value: bit0=1 (special), bits 1-2 = mode
		wantSlot0 byte
		wantSlot1 byte
		wantSlot2 byte
		wantSlot3 byte
	}{
		// mode 0 (val=0x01): banks 0,1,2,3
		{0x01, 0x10, 0x11, 0x12, 0x13},
		// mode 1 (val=0x03): banks 4,5,6,7
		{0x03, 0x14, 0x15, 0x16, 0x17},
		// mode 2 (val=0x05): banks 4,5,6,3
		{0x05, 0x14, 0x15, 0x16, 0x13},
		// mode 3 (val=0x07): banks 4,7,6,3
		{0x07, 0x14, 0x17, 0x16, 0x13},
	}

	for _, tt := range tests {
		mem.PageMemoryPlus3(tt.val)

		got0 := mem.Read(0x0000)
		got1 := mem.Read(0x4000)
		got2 := mem.Read(0x8000)
		got3 := mem.Read(0xC000)

		if got0 != tt.wantSlot0 {
			t.Errorf("mode val=0x%02X slot0: expected 0x%02X, got 0x%02X", tt.val, tt.wantSlot0, got0)
		}
		if got1 != tt.wantSlot1 {
			t.Errorf("mode val=0x%02X slot1: expected 0x%02X, got 0x%02X", tt.val, tt.wantSlot1, got1)
		}
		if got2 != tt.wantSlot2 {
			t.Errorf("mode val=0x%02X slot2: expected 0x%02X, got 0x%02X", tt.val, tt.wantSlot2, got2)
		}
		if got3 != tt.wantSlot3 {
			t.Errorf("mode val=0x%02X slot3: expected 0x%02X, got 0x%02X", tt.val, tt.wantSlot3, got3)
		}
	}
}

// =============================================================================
// TestPageMemoryPlus3_NormalMode — exit special mode
// =============================================================================

func TestPageMemoryPlus3_NormalMode(t *testing.T) {
	testDir := "test_roms_plus3norm"
	createTestROMsPlus(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("Failed to create +3 memory: %v", err)
	}

	// Enable special paging mode 0
	mem.PageMemoryPlus3(0x01)

	// Now exit special mode (bit 0 clear)
	mem.PageMemoryPlus3(0x00)

	// In normal mode, slot 1 should be RAM page 5, slot 2 should be RAM page 2
	// Write to RAM page 5 at slot 1 (0x4000)
	mem.Write(0x4000, 0x42)
	if mem.Read(0x4000) != 0x42 {
		t.Errorf("Normal mode: expected 0x42 at 0x4000 after exiting special paging")
	}

	// Slot 2 (0x8000) should be RAM page 2
	mem.Write(0x8000, 0x55)
	if mem.Read(0x8000) != 0x55 {
		t.Errorf("Normal mode: expected 0x55 at 0x8000 after exiting special paging")
	}
}

// =============================================================================
// TestContentionTiming_DifferentPositions
// =============================================================================

func TestContentionTiming_DifferentPositions(t *testing.T) {
	testDir := "test_roms_contend"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	var tstates uint64
	mem.TStates = &tstates
	mem.ContentionEnabled = true

	// Table of (tstate, expected delay > 0)
	tests := []struct {
		tstate      uint64
		addr        uint16
		expectDelay bool
	}{
		{14335, 0x4000, true},  // start of active display, contended address
		{14335, 0x8000, false}, // non-contended address
		{0, 0x4000, false},     // before active display
		{57344, 0x4000, false}, // after active display
		// Inside a scanline during pixel drawing (pos < 128)
		{14335 + 10, 0x4000, true},
		// Inside a scanline outside pixel drawing (pos >= 128)
		{14335 + 200, 0x4000, false},
	}

	for _, tt := range tests {
		tstates = tt.tstate
		before := tstates
		mem.ContendMemory(tt.addr)
		delayed := tstates > before

		if delayed != tt.expectDelay {
			t.Errorf("ContendMemory at tstate=%d addr=0x%04X: expectDelay=%v, gotDelay=%v",
				tt.tstate, tt.addr, tt.expectDelay, delayed)
		}
	}
}

// =============================================================================
// TestContentionDisabled
// =============================================================================

func TestContentionDisabled(t *testing.T) {
	testDir := "test_roms_nodisable"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	var tstates uint64
	mem.TStates = &tstates
	mem.ContentionEnabled = false // explicitly disabled

	tstates = 14335
	before := tstates
	mem.ContendMemory(0x4000) // contended address, active display period

	if tstates != before {
		t.Errorf("ContentionDisabled: expected no delay when ContentionEnabled=false, got delay=%d", tstates-before)
	}

	// Also test nil TStates pointer (even with enabled, should be no-op)
	mem.ContentionEnabled = true
	mem.TStates = nil
	before = 14335 // just a local var, not the tstates pointer
	_ = before
	// Should not panic
	mem.ContendMemory(0x4000)
}
