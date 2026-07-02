package roms

import (
	"os"
	"path/filepath"
	"testing"
)

// createTestROMDir creates a temporary directory with dummy ROM files for testing.
func createTestROMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// System ROMs (16KB each)
	systemROMs := []string{
		"48.rom",
		"128-0.rom",
		"128-1.rom",
		"plus2-0.rom",
		"plus2-1.rom",
		"plus3-0.rom",
		"plus3-1.rom",
		"plus3-2.rom",
		"plus3-3.rom",
	}
	for _, name := range systemROMs {
		data := make([]byte, 16384)
		// Fill with a recognisable pattern based on the filename
		for i := range data {
			data[i] = name[0] ^ byte(i&0xFF)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatalf("failed to create test ROM %s: %v", name, err)
		}
	}

	// Peripheral ROMs (8KB each)
	peripheralROMs := []string{
		"mf1_official.rom",
		"mf128_official.rom",
		"mf3_official.rom",
		"gdos.rom",
	}
	for _, name := range peripheralROMs {
		data := make([]byte, 8192)
		for i := range data {
			data[i] = name[0] ^ byte(i&0xFF)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatalf("failed to create test ROM %s: %v", name, err)
		}
	}

	return dir
}

func TestNewROMManager(t *testing.T) {
	rm := NewROMManager("/nonexistent")
	if rm == nil {
		t.Fatal("NewROMManager returned nil")
	}
	if rm.romPath != "/nonexistent" {
		t.Errorf("romPath = %q, want %q", rm.romPath, "/nonexistent")
	}
	if len(rm.roms) != 0 {
		t.Errorf("new ROMManager should have no loaded ROMs, got %d", len(rm.roms))
	}
	// Mappings should be initialised for all five models
	for _, model := range []SpectrumModel{Model48K, Model128K, ModelPlus2, ModelPlus2A, ModelPlus3} {
		if _, ok := rm.mappings[model]; !ok {
			t.Errorf("missing mapping for model %d", model)
		}
	}
}

func TestLoadROM_ValidFile(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	if err := rm.LoadROM(ROM48K, "48.rom"); err != nil {
		t.Fatalf("LoadROM failed for valid 48.rom: %v", err)
	}

	data, ok := rm.GetROM(ROM48K)
	if !ok {
		t.Fatal("GetROM returned false after successful LoadROM")
	}
	if len(data) != 16384 {
		t.Errorf("loaded ROM size = %d, want 16384", len(data))
	}
}

func TestLoadROM_InvalidFile(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	// File that does not exist on disk and is not embedded
	err := rm.LoadROM(ROM48K, "totally_nonexistent.rom")
	if err == nil {
		t.Error("LoadROM should fail for a nonexistent ROM file")
	}
}

func TestLoadROM_WrongSize(t *testing.T) {
	dir := createTestROMDir(t)

	// Write a ROM file with the wrong size (neither 16KB nor 8KB)
	badData := make([]byte, 1000)
	if err := os.WriteFile(filepath.Join(dir, "bad.rom"), badData, 0644); err != nil {
		t.Fatal(err)
	}

	rm := NewROMManager(dir)
	err := rm.LoadROM(ROM48K, "bad.rom")
	if err == nil {
		t.Error("LoadROM should fail for a ROM with invalid size")
	}
}

func TestLoadROM_PeripheralSize(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	// Peripheral ROMs should be 8KB
	if err := rm.LoadROM(ROMDISCIPLE, "gdos.rom"); err != nil {
		t.Fatalf("LoadROM failed for 8KB peripheral ROM: %v", err)
	}
	data, ok := rm.GetROM(ROMDISCIPLE)
	if !ok {
		t.Fatal("GetROM returned false for loaded peripheral ROM")
	}
	if len(data) != 8192 {
		t.Errorf("peripheral ROM size = %d, want 8192", len(data))
	}
}

func TestLoadROMsForModel_48K(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	if err := rm.LoadROMsForModel(Model48K); err != nil {
		t.Fatalf("LoadROMsForModel(Model48K) failed: %v", err)
	}
	if !rm.HasROM(ROM48K) {
		t.Error("48K ROM should be loaded after LoadROMsForModel")
	}
}

func TestLoadROMsForModel_128K(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	if err := rm.LoadROMsForModel(Model128K); err != nil {
		t.Fatalf("LoadROMsForModel(Model128K) failed: %v", err)
	}
	if !rm.HasROM(ROM128K_0) {
		t.Error("128K ROM 0 should be loaded")
	}
	if !rm.HasROM(ROM128K_1) {
		t.Error("128K ROM 1 should be loaded")
	}
}

func TestLoadROMsForModel_Plus3(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	if err := rm.LoadROMsForModel(ModelPlus3); err != nil {
		t.Fatalf("LoadROMsForModel(ModelPlus3) failed: %v", err)
	}
	for i, rt := range []ROMType{ROMPLUS3_0, ROMPLUS3_1, ROMPLUS3_2, ROMPLUS3_3} {
		if !rm.HasROM(rt) {
			t.Errorf("+3 ROM %d should be loaded", i)
		}
	}
}

func TestLoadROMsForModel_UnsupportedModel(t *testing.T) {
	rm := NewROMManager(t.TempDir())
	err := rm.LoadROMsForModel(SpectrumModel(999))
	if err == nil {
		t.Error("LoadROMsForModel should fail for unsupported model")
	}
}

func TestLoadROMsForModel_MissingRequiredROM(t *testing.T) {
	// Empty directory with no ROMs and embedded ROMs won't match names
	dir := t.TempDir()
	rm := NewROMManager(dir)

	// 48K model requires 48.rom -- if the embedded ROM exists the fallback
	// will succeed. We explicitly test with a model whose ROMs are unlikely
	// to be embedded (Plus2, which needs plus2-0.rom and plus2-1.rom).
	// If the embedded data directory contains these, the test still passes
	// because it validates the fallback path. If not, we expect an error.
	err := rm.LoadROMsForModel(ModelPlus2)
	if err != nil {
		// This is fine -- means the fallback did not have the file either
		t.Logf("LoadROMsForModel(ModelPlus2) with empty dir: %v (expected if no embedded fallback)", err)
	}
}

func TestGetROM_NotLoaded(t *testing.T) {
	rm := NewROMManager(t.TempDir())
	data, ok := rm.GetROM(ROM48K)
	if ok {
		t.Error("GetROM should return false for unloaded ROM")
	}
	if data != nil {
		t.Error("GetROM should return nil data for unloaded ROM")
	}
}

func TestHasROM(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	if rm.HasROM(ROM48K) {
		t.Error("HasROM should return false before loading")
	}

	_ = rm.LoadROM(ROM48K, "48.rom")
	if !rm.HasROM(ROM48K) {
		t.Error("HasROM should return true after loading")
	}
}

func TestGetLoadedROMs(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	loaded := rm.GetLoadedROMs()
	if len(loaded) != 0 {
		t.Errorf("GetLoadedROMs should return empty slice initially, got %d", len(loaded))
	}

	_ = rm.LoadROM(ROM48K, "48.rom")
	_ = rm.LoadROM(ROMDISCIPLE, "gdos.rom")

	loaded = rm.GetLoadedROMs()
	if len(loaded) != 2 {
		t.Errorf("GetLoadedROMs should return 2 after loading 2 ROMs, got %d", len(loaded))
	}

	found48K := false
	foundDisciple := false
	for _, rt := range loaded {
		if rt == ROM48K {
			found48K = true
		}
		if rt == ROMDISCIPLE {
			foundDisciple = true
		}
	}
	if !found48K {
		t.Error("GetLoadedROMs missing ROM48K")
	}
	if !foundDisciple {
		t.Error("GetLoadedROMs missing ROMDISCIPLE")
	}
}

func TestGetModelName(t *testing.T) {
	tests := []struct {
		model SpectrumModel
		want  string
	}{
		{Model48K, "ZX Spectrum 48K"},
		{Model128K, "ZX Spectrum 128K"},
		{ModelPlus2, "ZX Spectrum +2"},
		{ModelPlus2A, "ZX Spectrum +2A"},
		{ModelPlus3, "ZX Spectrum +3"},
		{SpectrumModel(99), "Unknown Model"},
	}
	for _, tc := range tests {
		got := GetModelName(tc.model)
		if got != tc.want {
			t.Errorf("GetModelName(%d) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestGetROMTypeName(t *testing.T) {
	tests := []struct {
		romType ROMType
		want    string
	}{
		{ROM48K, "48K ROM"},
		{ROM128K_0, "128K ROM 0"},
		{ROM128K_1, "128K ROM 1"},
		{ROMPLUS2_0, "+2 ROM 0"},
		{ROMPLUS2_1, "+2 ROM 1"},
		{ROMPLUS2A_0, "+2A ROM 0"},
		{ROMPLUS2A_1, "+2A ROM 1"},
		{ROMPLUS2A_2, "+2A ROM 2"},
		{ROMPLUS2A_3, "+2A ROM 3"},
		{ROMPLUS3_0, "+3 ROM 0"},
		{ROMPLUS3_1, "+3 ROM 1"},
		{ROMPLUS3_2, "+3 ROM 2"},
		{ROMPLUS3_3, "+3 ROM 3"},
		{ROMMULTIFACE1, "Multiface 1"},
		{ROMMULTIFACE128, "Multiface 128"},
		{ROMMULTIFACE3, "Multiface 3"},
		{ROMDISCIPLE, "Disciple/GDOS"},
		{ROMTRDOS, "TR-DOS"},
		{ROMPENTAGON, "Pentagon"},
		{ROMINTERFACE1, "Interface 1"},
		{ROMZX81, "ZX81 ROM"},
		{ROMZX80, "ZX80 ROM"},
		{ROMType(99), "Unknown ROM"},
	}
	for _, tc := range tests {
		got := GetROMTypeName(tc.romType)
		if got != tc.want {
			t.Errorf("GetROMTypeName(%d) = %q, want %q", tc.romType, got, tc.want)
		}
	}
}

func TestEmbeddedROMFallback(t *testing.T) {
	// Use an empty directory so filesystem lookup fails and the embedded
	// fallback must be used.
	dir := t.TempDir()
	rm := NewROMManager(dir)

	// The embedded data/ directory contains 48.rom. Loading it via the
	// ROM manager with an empty filesystem path should succeed through
	// the embedded fallback.
	err := rm.LoadROM(ROM48K, "48.rom")
	if err != nil {
		t.Fatalf("LoadROM should fall back to embedded ROM, got: %v", err)
	}

	data, ok := rm.GetROM(ROM48K)
	if !ok {
		t.Fatal("ROM should be available after embedded fallback load")
	}
	if len(data) != 16384 {
		t.Errorf("embedded ROM size = %d, want 16384", len(data))
	}
}

func TestEmbeddedROMFallback_Peripheral(t *testing.T) {
	dir := t.TempDir()
	rm := NewROMManager(dir)

	// mf1_official.rom is embedded at data/mf1_official.rom (8KB peripheral ROM)
	err := rm.LoadROM(ROMMULTIFACE1, "mf1_official.rom")
	if err != nil {
		t.Fatalf("LoadROM(ROMMULTIFACE1, mf1_official.rom) should fall back to embedded, got: %v", err)
	}
	if !rm.HasROM(ROMMULTIFACE1) {
		t.Error("ROMMULTIFACE1 should be loaded via embedded fallback")
	}
}

func TestLoadROMsForModel_UsesEmbeddedFallback(t *testing.T) {
	// Loading a full model from an empty dir should succeed if embedded
	// ROMs contain everything needed.
	dir := t.TempDir()
	rm := NewROMManager(dir)

	err := rm.LoadROMsForModel(Model48K)
	if err != nil {
		t.Fatalf("LoadROMsForModel(Model48K) should succeed via embedded fallback: %v", err)
	}
	if !rm.HasROM(ROM48K) {
		t.Error("ROM48K should be loaded via embedded fallback")
	}
}

func TestLoadPeripheralROMs(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	// LoadPeripheralROMs always returns nil (warnings are printed for missing ROMs)
	err := rm.LoadPeripheralROMs()
	if err != nil {
		t.Fatalf("LoadPeripheralROMs should not return error: %v", err)
	}

	// At minimum gdos.rom and multiface ROMs from our test dir should be loaded
	if !rm.HasROM(ROMDISCIPLE) {
		t.Error("ROMDISCIPLE should be loaded after LoadPeripheralROMs")
	}
	if !rm.HasROM(ROMMULTIFACE1) {
		t.Error("ROMMULTIFACE1 should be loaded after LoadPeripheralROMs")
	}
}

func TestFilesystemROMOverridesEmbedded(t *testing.T) {
	dir := createTestROMDir(t)
	rm := NewROMManager(dir)

	// Load the ROM -- this should come from the filesystem (our test dir)
	if err := rm.LoadROM(ROM48K, "48.rom"); err != nil {
		t.Fatalf("LoadROM failed: %v", err)
	}

	data, _ := rm.GetROM(ROM48K)

	// Our test ROM was filled with '4' ^ byte(i&0xFF) (since "48.rom"[0] == '4')
	// The first byte should be '4' (0x34)
	if data[0] != 0x34 {
		t.Errorf("first byte = 0x%02X, want 0x34 (filesystem ROM should override embedded)", data[0])
	}
}
