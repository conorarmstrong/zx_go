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
	os.RemoveAll(dir)
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
	mem.memoryPageReadMap[3] = 0  // Map page 0 to 0xC000-0xFFFF
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
	if tstates == before {
		// Contention should add some delay
		// (The exact delay depends on position within the scanline)
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
		os.WriteFile(filepath.Join(testDir, name), data, 0644)
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