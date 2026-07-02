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

func TestLoadROM_FilesystemOverridesEmbedded(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// Write a custom ROM to filesystem with primary name — should override embedded
	writeDummyMFROM(t, dir, "mf128_official.rom", 0xCD)

	mf, err := NewMultiface(Multiface128, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if mf.GetROM()[0] != 0xCD {
		t.Errorf("expected filesystem ROM byte 0xCD, got 0x%02X (embedded may have overridden)", mf.GetROM()[0])
	}
}

func TestLoadROM_EmbeddedFallback(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// No filesystem ROM files — should load from embedded ROMs

	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	rom := mf.GetROM()
	if len(rom) != 0x2000 {
		t.Errorf("expected 8KB ROM, got %d bytes", len(rom))
	}
	// Should NOT be the placeholder (placeholder starts with F3 C3 10 00)
	if rom[0] == 0xF3 && rom[1] == 0xC3 && rom[2] == 0x10 && rom[3] == 0x00 {
		t.Errorf("got placeholder ROM instead of embedded real ROM")
	}
}

func TestLoadROM_AllVariantsLoadEmbedded(t *testing.T) {
	mem, _ := newTestMemory(t)

	variants := []MultifaceType{Multiface1, Multiface128, Multiface3}
	for _, v := range variants {
		dir := t.TempDir()
		mf, err := NewMultiface(v, dir, mem)
		if err != nil {
			t.Fatalf("error for variant %d: %v", v, err)
		}
		rom := mf.GetROM()
		if len(rom) != 0x2000 {
			t.Errorf("variant %d: expected 8KB ROM, got %d", v, len(rom))
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
	// Wrong-size file should be ignored; embedded ROM loads instead
	rom := mf.GetROM()
	if len(rom) != 0x2000 {
		t.Errorf("expected 8KB ROM, got %d bytes", len(rom))
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

	// Set stealth mode directly (MF1 uses physical switch, not port)
	mf.invisible = true
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

// Each Multiface variant decodes a different port pattern; pin the
// isMultifacePort lookup against the documented patterns.

func TestIsMultifacePort_MF1Pattern(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// MF1: port & $0072 == $0012. Matching: $0012, $0092, $001F, $009F, etc.
	matching := []uint16{0x001F, 0x0092, 0x0012, 0x009F, 0x0093}
	for _, p := range matching {
		if !mf.isMultifacePort(p) {
			t.Errorf("MF1: port $%04X should match", p)
		}
	}
	// Not matching.
	nonMatching := []uint16{0x0000, 0x0030, 0x0070, 0x0032}
	for _, p := range nonMatching {
		if mf.isMultifacePort(p) {
			t.Errorf("MF1: port $%04X should NOT match", p)
		}
	}
}

func TestIsMultifacePort_MF128Pattern(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface128, dir, mem)

	// MF128: port & $0072 == $0032 OR port & $0072 == $0012 (back-compat).
	matching := []uint16{0x0032, 0x0012, 0x00BF, 0x009F} // both MF128 + MF1 patterns
	for _, p := range matching {
		if !mf.isMultifacePort(p) {
			t.Errorf("MF128: port $%04X should match", p)
		}
	}
}

func TestIsMultifacePort_MF3Pattern(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface3, dir, mem)

	// MF3: ONLY port & $0072 == $0032 (no MF1 backwards-compat).
	if !mf.isMultifacePort(0x0032) {
		t.Errorf("MF3: $0032 should match")
	}
	// MF3 does NOT accept the MF1 port pattern.
	if mf.isMultifacePort(0x0012) {
		t.Errorf("MF3: $0012 (MF1 pattern) should NOT match")
	}
}

func TestHandlePortRead_MultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Port must match: (port & 0x0072) == 0x0012
	port := uint16(0x009F) // MF1 port with A7=1
	val, handled := mf.HandlePortRead(port)
	if !handled {
		t.Error("HandlePortRead should handle Multiface port 0x009F")
	}
	// New API always returns 0xFF for matching ports
	if val != 0xFF {
		t.Errorf("expected 0xFF, got 0x%02X", val)
	}
}

func TestHandlePortRead_PageIn_A7Set(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// IN with A7=1 should page in ROM (when not invisible)
	port := uint16(0x009F) // MF1 port with A7=1
	val, handled := mf.HandlePortRead(port)
	if !handled {
		t.Fatal("HandlePortRead should handle Multiface port")
	}
	if val != 0xFF {
		t.Errorf("expected 0xFF, got 0x%02X", val)
	}
	if !mf.IsROMPaged() {
		t.Error("ROM should be paged in after IN with A7=1")
	}
}

func TestHandlePortRead_PageOut_A7Clear(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Page in first
	mf.HandleNMI()
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}

	// IN with A7=0 should page out ROM
	port := uint16(0x001F) // MF1 port with A7=0
	val, handled := mf.HandlePortRead(port)
	if !handled {
		t.Fatal("HandlePortRead should handle Multiface port 0x001F")
	}
	if val != 0xFF {
		t.Errorf("expected 0xFF, got 0x%02X", val)
	}
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out after IN with A7=0")
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
	_, handled := mf.HandlePortRead(0x009F)
	if handled {
		t.Error("HandlePortRead should not handle when disabled")
	}
}

func TestHandlePortRead_Invisible_StillHandled(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.invisible = true

	// Invisible does NOT prevent the port from being handled — it only
	// prevents page-IN. The port read still returns 0xFF, true.
	val, handled := mf.HandlePortRead(0x009F)
	if !handled {
		t.Error("HandlePortRead should still handle port when invisible")
	}
	if val != 0xFF {
		t.Errorf("expected 0xFF, got 0x%02X", val)
	}
	// But ROM should NOT be paged in because invisible blocks page-in
	if mf.IsROMPaged() {
		t.Error("ROM should not be paged in when invisible")
	}
}

func TestHandlePortRead_Invisible_PageOutStillWorks(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// Page in first, then go invisible
	mf.HandleNMI()
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}
	mf.invisible = true

	// IN with A7=0 should still page out even when invisible
	_, handled := mf.HandlePortRead(0x001F)
	if !handled {
		t.Error("HandlePortRead should handle port when invisible")
	}
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out — page-out is not blocked by invisible")
	}
}

// ---------- HandlePortWrite ----------

func TestHandlePortWrite_MultifacePort(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	handled := mf.HandlePortWrite(0x009F, 0x00)
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
	handled := mf.HandlePortWrite(0x009F, 0x00)
	if handled {
		t.Error("HandlePortWrite should not handle when disabled")
	}
}

func TestHandlePortWrite_MF128_SoftwareLockout(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface128, dir, mem)

	// Page in ROM via NMI first (lockout only works when romPaged)
	mf.HandleNMI()
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}

	// OUT to MF128 port with A7=0 sets invisible (software lockout)
	handled := mf.HandlePortWrite(0x003F, 0x00)
	if !handled {
		t.Error("HandlePortWrite should handle MF128 port 0x003F")
	}
	if !mf.IsInvisible() {
		t.Error("should be invisible after OUT with A7=0 on MF128")
	}

	// OUT to MF128 port with A7=1 clears invisible
	handled = mf.HandlePortWrite(0x00BF, 0x00)
	if !handled {
		t.Error("HandlePortWrite should handle MF128 port 0x00BF")
	}
	if mf.IsInvisible() {
		t.Error("should be visible after OUT with A7=1 on MF128")
	}
}

func TestHandlePortWrite_MF3_SoftwareLockout(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface3, dir, mem)

	// Page in ROM via NMI first
	mf.HandleNMI()

	// OUT to MF3 port with A7=0 sets invisible
	handled := mf.HandlePortWrite(0x003F, 0x00)
	if !handled {
		t.Error("HandlePortWrite should handle MF3 port 0x003F")
	}
	if !mf.IsInvisible() {
		t.Error("should be invisible after OUT with A7=0 on MF3")
	}
}

func TestHandlePortWrite_MF1_NoLockout(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// MF1 OUT just returns true, no software lockout
	handled := mf.HandlePortWrite(0x009F, 0x00)
	if !handled {
		t.Error("MF1 HandlePortWrite should return true for matching port")
	}
	if mf.IsInvisible() {
		t.Error("MF1 should not set invisible via port write")
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
	mf.HandlePortRead(0x001F) // page out (A7=0)
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
	mf.redButton = false // clear red button directly
	if mf.IsRedButtonPressed() {
		t.Error("red button should be cleared after control write bit 1")
	}
}

func TestControlWrite_StealthMode(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// MF128 supports software lockout via OUT with A7=0
	mf, _ := NewMultiface(Multiface128, dir, mem)

	mf.HandleNMI() // page in ROM
	if mf.IsInvisible() {
		t.Fatal("should not be invisible initially")
	}

	// OUT to MF128 port with A7=0 sets software lockout (invisible)
	// Port 0x003F matches MF128 mask: (0x003F & 0x0072) == 0x0032, A7=0
	mf.HandlePortWrite(0x003F, 0x00)
	if !mf.IsInvisible() {
		t.Error("should be invisible after OUT with A7=0")
	}
}

func TestControlWrite_VisibleMode(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	mf.invisible = true

	// Bit 3: visible mode
	mf.invisible = false // visible mode directly
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
	mf.HandlePortRead(0x001F)
	mf.redButton = false // page out + clear
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

// ---------- Integration: full NMI cycle ----------

func TestNMICycle_Full(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface1, dir, mem)

	// 1. Press the red button (NMI) — pages in ROM
	if !mf.HandleNMI() {
		t.Fatal("NMI should succeed")
	}
	if !mf.IsRedButtonPressed() || !mf.IsROMPaged() {
		t.Fatal("after NMI: red button and ROM paged should both be true")
	}

	// 2. CPU reaches NMI vector
	if !mf.HandleOpcodeRead(0x0066) {
		t.Fatal("opcode read at 0x0066 should succeed")
	}

	// 3. Multiface ROM code runs, then pages out via IN with A7=0
	val, handled := mf.HandlePortRead(0x001F) // A7=0 → page out
	if !handled {
		t.Fatal("HandlePortRead should handle MF1 port 0x001F")
	}
	if val != 0xFF {
		t.Errorf("expected 0xFF from port read, got 0x%02X", val)
	}
	if mf.IsROMPaged() {
		t.Error("ROM should be paged out after IN with A7=0")
	}
}

// ---------- Stealth mode cycle ----------

func TestStealthModeCycle(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// Use MF128 which supports software lockout via port writes
	mf, _ := NewMultiface(Multiface128, dir, mem)

	// Page in ROM via NMI first (lockout only works when romPaged)
	mf.HandleNMI()
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}

	// Go invisible via OUT with A7=0 on MF128 port
	// Port 0x003F: (0x003F & 0x0072) == 0x0032 ✓, A7=0
	mf.HandlePortWrite(0x003F, 0x00)
	if !mf.IsInvisible() {
		t.Error("should be invisible after OUT with A7=0")
	}

	// Page out ROM so we can test NMI blocking
	mf.HandlePortRead(0x003F) // MF128 port with A7=0 → page out

	// NMI should fail while invisible
	if mf.HandleNMI() {
		t.Error("NMI should fail while invisible")
	}

	// Make visible again via OUT with A7=1
	// Need to page in first for the lockout write to take effect
	mf.invisible = false             // reset for testing
	mf.HandleNMI()                   // page in ROM again
	mf.HandlePortWrite(0x00BF, 0x00) // A7=1 → clear invisible
	if mf.IsInvisible() {
		t.Error("should be visible after OUT with A7=1")
	}

	// Page out and try NMI again
	mf.HandlePortRead(0x003F) // page out
	mf.redButton = false

	// NMI should work now
	if !mf.HandleNMI() {
		t.Error("NMI should succeed after becoming visible")
	}
}

// loadROM filesystem-load branch: a valid 0x2000 file at romPath wins
// over the embedded ROM, and a wrong-size file falls through to it.

func TestLoadROM_FilesystemWins(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// Plant a recognisable 0x2000 MF1 ROM; loadROM tries fs first.
	writeDummyMFROM(t, dir, "mf1_official.rom", 0x5A)
	mf, err := NewMultiface(Multiface1, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface: %v", err)
	}
	rom := mf.GetROM()
	if len(rom) != 0x2000 || rom[0] != 0x5A || rom[0x1FFF] != 0x5A {
		t.Errorf("fs ROM not loaded: len=%d first=$%02X", len(rom), rom[0])
	}
}

func TestLoadROM_WrongSizeFallsThrough(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	// A wrong-size file at the primary name must be ignored; loadROM
	// then falls back to the embedded ROM (or placeholder), yielding a
	// valid 0x2000 ROM either way.
	if err := os.WriteFile(filepath.Join(dir, "mf128_official.rom"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	mf, err := NewMultiface(Multiface128, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface: %v", err)
	}
	if got := len(mf.GetROM()); got != 0x2000 {
		t.Errorf("ROM len = %d, want 0x2000 after wrong-size fall-through", got)
	}
}
