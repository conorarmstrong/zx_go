package multiface

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// helper creates a temp dir with a dummy 48.rom and returns a *memory.Memory plus cleanup func.
func newTestMemory(t *testing.T) (*memory.Memory, func()) {
	t.Helper()
	dir := t.TempDir()

	// memory.New will fall back to embedded ROMs, but we need a rom path that
	// exists so the manager can initialise. The embedded 48.rom will be used.
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("failed to create test memory: %v", err)
	}
	return mem, func() {} // TempDir is cleaned up automatically
}

// writeDummyMFROM writes an 8KB file filled with the given byte value.
func writeDummyMFROM(t *testing.T, dir, name string, fill byte) {
	t.Helper()
	data := make([]byte, 0x2000)
	for i := range data {
		data[i] = fill
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatalf("failed to write dummy ROM %s: %v", name, err)
	}
}

// ---------- NewMultiface creation ----------

func TestNewMultiface_Multiface1(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()

	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(Multiface1) error: %v", err)
	}
	if mf.GetVariant() != Multiface1 {
		t.Errorf("expected variant Multiface1, got %d", mf.GetVariant())
	}
	if !mf.IsEnabled() {
		t.Error("expected Multiface to be enabled after creation")
	}
	if mf.IsROMPaged() {
		t.Error("ROM should not be paged in after creation")
	}
	if mf.IsInvisible() {
		t.Error("should not be invisible after creation")
	}
	if mf.IsRedButtonPressed() {
		t.Error("red button should not be pressed after creation")
	}
	// ROM should be 8KB (placeholder)
	if len(mf.GetROM()) != 0x2000 {
		t.Errorf("expected 8KB ROM, got %d bytes", len(mf.GetROM()))
	}
	// RAM should be 8KB
	if len(mf.GetRAM()) != 0x2000 {
		t.Errorf("expected 8KB RAM, got %d bytes", len(mf.GetRAM()))
	}
}

func TestNewMultiface_Multiface128(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()

	mf, err := NewMultiface(Multiface128, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(Multiface128) error: %v", err)
	}
	if mf.GetVariant() != Multiface128 {
		t.Errorf("expected variant Multiface128, got %d", mf.GetVariant())
	}
	if mf.romFile != "mf128_official.rom" {
		t.Errorf("expected romFile mf128_official.rom, got %s", mf.romFile)
	}
}

func TestNewMultiface_Multiface3(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()

	mf, err := NewMultiface(Multiface3, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(Multiface3) error: %v", err)
	}
	if mf.GetVariant() != Multiface3 {
		t.Errorf("expected variant Multiface3, got %d", mf.GetVariant())
	}
	if mf.romFile != "mf3_official.rom" {
		t.Errorf("expected romFile mf3_official.rom, got %s", mf.romFile)
	}
}

func TestNewMultiface_DefaultVariant(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()

	// Use a bogus variant value to exercise the default branch
	mf, err := NewMultiface(MultifaceType(99), dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(99) error: %v", err)
	}
	if mf.romFile != "mf1_official.rom" {
		t.Errorf("expected default romFile mf1_official.rom, got %s", mf.romFile)
	}
}

// ---------- ROM loading ----------

func TestLoadROM_SpecificFile(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	writeDummyMFROM(t, dir, "mf1_official.rom", 0xAB)

	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// The ROM should contain 0xAB bytes, not the placeholder
	if mf.GetROM()[0] != 0xAB {
		t.Errorf("expected ROM byte 0xAB, got 0x%02X", mf.GetROM()[0])
	}
}

func TestLoadROM_AlternativeName(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// Do not write mf128_official.rom; write one of the alternative names instead
	writeDummyMFROM(t, dir, "multiface.rom", 0xCD)

	mf, err := NewMultiface(Multiface128, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if mf.GetROM()[0] != 0xCD {
		t.Errorf("expected ROM byte 0xCD from alternative name, got 0x%02X", mf.GetROM()[0])
	}
}

func TestLoadROM_PlaceholderFallback(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// No ROM files at all -- should get placeholder

	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Placeholder starts with DI (0xF3), JP (0xC3), 0x10, 0x00
	rom := mf.GetROM()
	if rom[0] != 0xF3 || rom[1] != 0xC3 || rom[2] != 0x10 || rom[3] != 0x00 {
		t.Errorf("unexpected placeholder ROM header: %02X %02X %02X %02X", rom[0], rom[1], rom[2], rom[3])
	}
	// Version byte at 0x0011 should be 0x01 for Multiface1
	if rom[0x0011] != 0x01 {
		t.Errorf("placeholder version byte: expected 0x01, got 0x%02X", rom[0x0011])
	}
}

func TestLoadROM_PlaceholderVersions(t *testing.T) {
	mem, _ := newTestMemory(t)

	tests := []struct {
		variant     MultifaceType
		expectedVer byte
	}{
		{Multiface1, 0x01},
		{Multiface128, 0x02},
		{Multiface3, 0x03},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		mf, err := NewMultiface(tc.variant, dir, mem)
		if err != nil {
			t.Fatalf("error for variant %d: %v", tc.variant, err)
		}
		if mf.GetROM()[0x0011] != tc.expectedVer {
			t.Errorf("variant %d: expected version 0x%02X, got 0x%02X",
				tc.variant, tc.expectedVer, mf.GetROM()[0x0011])
		}
	}
}

func TestLoadROM_WrongSizeIgnored(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// Write a file with incorrect size (not 8KB)
	if err := os.WriteFile(filepath.Join(dir, "mf1_official.rom"), make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}

	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should fall through to placeholder since file is wrong size
	rom := mf.GetROM()
	if rom[0] != 0xF3 {
		t.Error("expected placeholder ROM when specific file has wrong size")
	}
}

// ---------- HandleNMI ----------

func TestHandleNMI_Enabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	result := mf.HandleNMI()
	if !result {
		t.Error("HandleNMI should return true when enabled")
	}
	if !mf.IsRedButtonPressed() {
		t.Error("red button should be pressed after HandleNMI")
	}
	if !mf.IsROMPaged() {
		t.Error("ROM should be paged in after HandleNMI")
	}
}

func TestHandleNMI_Disabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.SetEnabled(false)
	result := mf.HandleNMI()
	if result {
		t.Error("HandleNMI should return false when disabled")
	}
	if mf.IsRedButtonPressed() {
		t.Error("red button should not be pressed when disabled")
	}
}

func TestHandleNMI_Invisible(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Set stealth mode via control write
	mf.handleControlWrite(0x04)
	if !mf.IsInvisible() {
		t.Fatal("should be invisible after writing 0x04")
	}

	result := mf.HandleNMI()
	if result {
		t.Error("HandleNMI should return false when invisible")
	}
}

// ---------- HandleOpcodeRead ----------

func TestHandleOpcodeRead_NMIVector(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Must press red button first for opcode read to trigger
	mf.HandleNMI()
	// Reset romPaged for the test
	mf.romPaged = false

	result := mf.HandleOpcodeRead(NMIVector1)
	if !result {
		t.Error("HandleOpcodeRead should return true at 0x0066")
	}
	if !mf.IsROMPaged() {
		t.Error("ROM should be paged in after opcode read at NMI vector")
	}
}

func TestHandleOpcodeRead_NMIVector2(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI()
	mf.romPaged = false

	result := mf.HandleOpcodeRead(NMIVector2)
	if !result {
		t.Error("HandleOpcodeRead should return true at 0x0067")
	}
	if !mf.IsROMPaged() {
		t.Error("ROM should be paged in after opcode read at 0x0067")
	}
}

func TestHandleOpcodeRead_OtherAddress(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI()

	result := mf.HandleOpcodeRead(0x1234)
	if result {
		t.Error("HandleOpcodeRead should return false at non-NMI address")
	}
}

func TestHandleOpcodeRead_NoRedButton(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Don't press red button
	result := mf.HandleOpcodeRead(NMIVector1)
	if result {
		t.Error("HandleOpcodeRead should return false when red button not pressed")
	}
}

func TestHandleOpcodeRead_Disabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI()
	mf.SetEnabled(false)

	result := mf.HandleOpcodeRead(NMIVector1)
	if result {
		t.Error("HandleOpcodeRead should return false when disabled")
	}
}

func TestHandleOpcodeRead_Invisible(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI()
	mf.invisible = true

	result := mf.HandleOpcodeRead(NMIVector1)
	if result {
		t.Error("HandleOpcodeRead should return false when invisible")
	}
}

// ---------- HandlePortRead ----------

func TestHandlePortRead_MultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Port must match: (port & 0x3C) == 0x3C
	port := uint16(0x003C) // Matches the mask
	val, handled := mf.HandlePortRead(port)
	if !handled {
		t.Error("HandlePortRead should handle Multiface port 0x003C")
	}
	// Initially: romPaged=false, redButton=false => status=0
	if val != 0x00 {
		t.Errorf("expected status 0x00, got 0x%02X", val)
	}
}

func TestHandlePortRead_StatusBits(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // sets redButton=true, romPaged=true

	port := uint16(0x003C)
	val, handled := mf.HandlePortRead(port)
	if !handled {
		t.Fatal("HandlePortRead should handle Multiface port")
	}
	// romPaged=true -> bit 0 set, redButton=true -> bit 1 set => 0x03
	if val != 0x03 {
		t.Errorf("expected status 0x03, got 0x%02X", val)
	}
}

func TestHandlePortRead_OnlyROMPaged(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.romPaged = true
	mf.redButton = false

	port := uint16(0x003C)
	val, _ := mf.HandlePortRead(port)
	if val != 0x01 {
		t.Errorf("expected status 0x01 (ROM paged only), got 0x%02X", val)
	}
}

func TestHandlePortRead_NonMultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Port that does not match the mask
	_, handled := mf.HandlePortRead(0x0000)
	if handled {
		t.Error("HandlePortRead should not handle non-Multiface ports")
	}
}

func TestHandlePortRead_Disabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.SetEnabled(false)
	_, handled := mf.HandlePortRead(0x003C)
	if handled {
		t.Error("HandlePortRead should not handle when disabled")
	}
}

func TestHandlePortRead_Invisible(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.invisible = true
	_, handled := mf.HandlePortRead(0x003C)
	if handled {
		t.Error("HandlePortRead should not handle when invisible")
	}
}

// ---------- HandlePortWrite ----------

func TestHandlePortWrite_MultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	handled := mf.HandlePortWrite(0x003C, 0x00)
	if !handled {
		t.Error("HandlePortWrite should handle Multiface port")
	}
}

func TestHandlePortWrite_NonMultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	handled := mf.HandlePortWrite(0x0000, 0x00)
	if handled {
		t.Error("HandlePortWrite should not handle non-Multiface port 0x0000")
	}
}

func TestHandlePortWrite_Disabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.SetEnabled(false)
	handled := mf.HandlePortWrite(0x003C, 0x00)
	if handled {
		t.Error("HandlePortWrite should not handle when disabled")
	}
}

func TestHandlePortWrite_VideoPort_Multiface128(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface128, dir, mem)

	// Video port matches: (port & 0x8002) == 0x0000
	// Port 0x0000 matches but also matches PortBase check first if (0x0000 & 0x3C) == 0x3C? No.
	// 0x0000 & 0x3C = 0x00 != 0x3C, so first check fails.
	// 0x0000 & 0x8002 = 0x0000 == 0x0000, so video port matches.
	handled := mf.HandlePortWrite(0x0000, 0x08) // bit 3 set
	if !handled {
		t.Error("HandlePortWrite should handle video port on Multiface128")
	}
	if mf.videoPageStore != 0x01 {
		t.Errorf("videoPageStore: expected 0x01, got 0x%02X", mf.videoPageStore)
	}
}

func TestHandlePortWrite_VideoPort_Multiface3(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface3, dir, mem)

	handled := mf.HandlePortWrite(0x0000, 0x00) // bit 3 clear
	if !handled {
		t.Error("HandlePortWrite should handle video port on Multiface3")
	}
	if mf.videoPageStore != 0x00 {
		t.Errorf("videoPageStore: expected 0x00, got 0x%02X", mf.videoPageStore)
	}
}

func TestHandlePortWrite_VideoPort_Multiface1_Ignored(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Multiface1 should NOT handle video port
	handled := mf.HandlePortWrite(0x0000, 0x08)
	if handled {
		t.Error("Multiface1 should not handle video port writes")
	}
}

// ---------- Control register bits ----------

func TestControlWrite_ROMPageOut(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // pages in ROM
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}

	// Bit 0: page out ROM
	mf.handleControlWrite(0x01)
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out after control write bit 0")
	}
}

func TestControlWrite_ClearRedButton(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // sets red button
	if !mf.IsRedButtonPressed() {
		t.Fatal("red button should be pressed after NMI")
	}

	// Bit 1: clear red button
	mf.handleControlWrite(0x02)
	if mf.IsRedButtonPressed() {
		t.Error("red button should be cleared after control write bit 1")
	}
}

func TestControlWrite_StealthMode(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // page in ROM
	if mf.IsInvisible() {
		t.Fatal("should not be invisible initially")
	}

	// Bit 2: stealth mode (also pages out ROM)
	mf.handleControlWrite(0x04)
	if !mf.IsInvisible() {
		t.Error("should be invisible after control write bit 2")
	}
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out when entering stealth mode")
	}
}

func TestControlWrite_VisibleMode(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.invisible = true

	// Bit 3: visible mode
	mf.handleControlWrite(0x08)
	if mf.IsInvisible() {
		t.Error("should not be invisible after control write bit 3")
	}
}

func TestControlWrite_CombinedBits(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // romPaged=true, redButton=true

	// Write 0x03 = page out ROM + clear red button
	mf.handleControlWrite(0x03)
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out")
	}
	if mf.IsRedButtonPressed() {
		t.Error("red button should be cleared")
	}
}

// ---------- SetEnabled ----------

func TestSetEnabled_DisableCleanup(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // romPaged=true, redButton=true

	mf.SetEnabled(false)
	if mf.IsEnabled() {
		t.Error("should be disabled")
	}
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out when disabling")
	}
	if mf.IsRedButtonPressed() {
		t.Error("red button should be cleared when disabling")
	}
}

func TestSetEnabled_Enable(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.SetEnabled(false)
	mf.SetEnabled(true)
	if !mf.IsEnabled() {
		t.Error("should be enabled")
	}
}

// ---------- pageInROM edge cases ----------

func TestPageInROM_WhenInvisible(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.invisible = true
	mf.pageInROM()
	if mf.IsROMPaged() {
		t.Error("ROM should not page in when invisible")
	}
}

func TestPageInROM_WhenDisabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.enabled = false
	mf.pageInROM()
	if mf.IsROMPaged() {
		t.Error("ROM should not page in when disabled")
	}
}

// ---------- GetVariantName ----------

func TestGetVariantName(t *testing.T) {
	tests := []struct {
		variant  MultifaceType
		expected string
	}{
		{Multiface1, "Multiface 1"},
		{Multiface128, "Multiface 128"},
		{Multiface3, "Multiface 3"},
		{MultifaceType(99), "Multiface 1"}, // default
	}
	for _, tc := range tests {
		name := GetVariantName(tc.variant)
		if name != tc.expected {
			t.Errorf("GetVariantName(%d): expected %q, got %q", tc.variant, tc.expected, name)
		}
	}
}

// ---------- GetCompatibleModel ----------

func TestGetCompatibleModel(t *testing.T) {
	mem, _ := newTestMemory(t)

	tests := []struct {
		variant  MultifaceType
		expected roms.SpectrumModel
	}{
		{Multiface1, roms.Model48K},
		{Multiface128, roms.Model128K},
		{Multiface3, roms.ModelPlus3},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		mf, _ := NewMultiface(tc.variant, dir, mem)
		model := mf.GetCompatibleModel()
		if model != tc.expected {
			t.Errorf("variant %d: expected model %d, got %d", tc.variant, tc.expected, model)
		}
	}
}

// ---------- SaveSnapshot / LoadSnapshot ----------

func TestSaveSnapshot_NotActive(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	err := mf.SaveSnapshot("test.sna")
	if err == nil {
		t.Error("SaveSnapshot should fail when Multiface is not active")
	}
}

func TestSaveSnapshot_Active(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.HandleNMI() // makes it active
	err := mf.SaveSnapshot("test.sna")
	// Currently returns "not yet implemented"
	if err == nil {
		t.Error("SaveSnapshot should return error (not yet implemented)")
	}
}

func TestLoadSnapshot_Disabled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.SetEnabled(false)
	err := mf.LoadSnapshot("test.sna")
	if err == nil {
		t.Error("LoadSnapshot should fail when disabled")
	}
}

func TestLoadSnapshot_FileNotFound(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	err := mf.LoadSnapshot(filepath.Join(dir, "nonexistent.sna"))
	if err == nil {
		t.Error("LoadSnapshot should fail when file does not exist")
	}
}

func TestLoadSnapshot_FileExists(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Create a dummy snapshot file
	snapFile := filepath.Join(dir, "test.sna")
	if err := os.WriteFile(snapFile, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	err := mf.LoadSnapshot(snapFile)
	// Should fail with "not yet implemented"
	if err == nil {
		t.Error("LoadSnapshot should return error (not yet implemented)")
	}
}

// ---------- Integration: full NMI cycle ----------

func TestNMICycle_Full(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// 1. Press the red button (NMI)
	if !mf.HandleNMI() {
		t.Fatal("NMI should succeed")
	}
	if !mf.IsRedButtonPressed() || !mf.IsROMPaged() {
		t.Fatal("after NMI: red button and ROM paged should both be true")
	}

	// 2. Read status port
	status, _ := mf.HandlePortRead(0x003C)
	if status != 0x03 {
		t.Errorf("status should be 0x03 (ROM paged + red button), got 0x%02X", status)
	}

	// 3. CPU reaches NMI vector
	if !mf.HandleOpcodeRead(0x0066) {
		t.Fatal("opcode read at 0x0066 should succeed")
	}

	// 4. Multiface code pages out ROM and clears red button
	mf.HandlePortWrite(0x003C, 0x03) // bits 0 + 1

	if mf.IsROMPaged() {
		t.Error("ROM should be paged out after control write")
	}
	if mf.IsRedButtonPressed() {
		t.Error("red button should be cleared after control write")
	}

	// 5. Read status again
	status, _ = mf.HandlePortRead(0x003C)
	if status != 0x00 {
		t.Errorf("status should be 0x00 after cleanup, got 0x%02X", status)
	}
}

// ---------- Stealth mode cycle ----------

func TestStealthModeCycle(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Go invisible
	mf.HandlePortWrite(0x003C, 0x04) // bit 2 = stealth
	if !mf.IsInvisible() {
		t.Error("should be invisible")
	}

	// NMI should fail while invisible
	if mf.HandleNMI() {
		t.Error("NMI should fail while invisible")
	}

	// Make visible again
	mf.HandlePortWrite(0x003C, 0x08) // bit 3 = visible
	if mf.IsInvisible() {
		t.Error("should be visible again")
	}

	// NMI should work now
	if !mf.HandleNMI() {
		t.Error("NMI should succeed after becoming visible")
	}
}
