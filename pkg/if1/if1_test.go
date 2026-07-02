package if1

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeROM returns an 8KB ROM image where each byte is the low 8 bits
// of its address. Lets the tests confirm a specific byte came from
// the IF1 ROM rather than from a uninitialised buffer.
func fakeROM() []byte {
	rom := make([]byte, ROMSize)
	for i := range rom {
		rom[i] = byte(i & 0xFF)
	}
	return rom
}

func TestNewIF1IsInactive(t *testing.T) {
	i := New()
	if i.Active() {
		t.Error("fresh IF1 is active")
	}
	if i.HasROM() {
		t.Error("fresh IF1 has a ROM loaded")
	}
}

func TestLoadROMBytesValidatesSize(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(make([]byte, 100)); !errors.Is(err, ErrROMSize) {
		t.Errorf("LoadROMBytes(100): err = %v, want ErrROMSize", err)
	}
	if err := i.LoadROMBytes(make([]byte, ROMSize)); err != nil {
		t.Errorf("LoadROMBytes(8192): err = %v, want nil", err)
	}
	if !i.HasROM() {
		t.Error("after successful load: HasROM = false")
	}
}

func TestPageInRequiresROM(t *testing.T) {
	i := New()
	i.PageIn()
	if i.Active() {
		t.Error("PageIn without ROM activated the IF1")
	}
}

// TestMemoryReadOverlay verifies that when the IF1 is active, reads
// in the ROM area return IF1 ROM bytes (with the 8 KB mirror
// behaviour) and that out-of-range and inactive states fall through.
func TestMemoryReadOverlay(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}

	// Inactive: never returns a value, even in the ROM range.
	if _, ok := i.MemoryRead(0x0000); ok {
		t.Error("inactive IF1: claimed 0x0000")
	}

	// Activate and read from both the lower and upper 8 KB halves
	// of the 16 KB ROM area. Both should map to the same 8 KB ROM
	// byte (mirrored).
	i.PageIn()
	if !i.Active() {
		t.Fatal("after PageIn with ROM: not active")
	}
	val, ok := i.MemoryRead(0x0010)
	if !ok || val != 0x10 {
		t.Errorf("MemoryRead(0x0010) = (%02X, %v), want (0x10, true)", val, ok)
	}
	val, ok = i.MemoryRead(0x2010) // upper half — same byte after mirror
	if !ok || val != 0x10 {
		t.Errorf("MemoryRead(0x2010) = (%02X, %v), want (0x10, true) (mirror)", val, ok)
	}

	// Above 0x4000 → fall through (RAM area).
	if _, ok := i.MemoryRead(0x8000); ok {
		t.Error("MemoryRead(0x8000): claimed RAM area")
	}

	// PageOut and confirm the overlay drops.
	i.PageOut()
	if _, ok := i.MemoryRead(0x0010); ok {
		t.Error("after PageOut: MemoryRead still claiming")
	}
}

func TestHandlePortDelegatesToULA(t *testing.T) {
	i := New()

	// Unknown port — fall through.
	if _, ok := i.HandlePortRead(0xFE); ok {
		t.Error("HandlePortRead(0xFE): claimed by IF1")
	}
	// CTR port — claimed by IF1 (returns the no-peripherals stub value).
	if _, ok := i.HandlePortRead(PortCTR); !ok {
		t.Error("HandlePortRead(CTR): not claimed by IF1")
	}
}

// TestPreFetchHookTriggersPageIn verifies the page-in addresses fire
// the overlay, and unrelated PCs don't.
func TestPreFetchHookTriggersPageIn(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}
	for _, pc := range []uint16{PageInRST8, PageInCloseChannel} {
		i.PageOut()
		i.PreFetchHook(pc)
		if !i.Active() {
			t.Errorf("PreFetchHook(0x%04X) did not page in", pc)
		}
	}
	i.PageOut()
	i.PreFetchHook(0x1234)
	if i.Active() {
		t.Errorf("PreFetchHook(0x1234) erroneously paged in")
	}
}

// TestPostFetchHookTriggersPageOut verifies the page-out address
// drops the overlay.
func TestPostFetchHookTriggersPageOut(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}
	i.PageIn()
	i.PostFetchHook(PageOutAddr)
	if i.Active() {
		t.Errorf("PostFetchHook(0x%04X) did not page out", PageOutAddr)
	}
}

// TestFetchHooksNoOpWithoutROM ensures the hooks are safe to install
// when no ROM is loaded — they just don't do anything.
func TestFetchHooksNoOpWithoutROM(t *testing.T) {
	i := New()
	i.PreFetchHook(0x0008)  // would page in if a ROM were loaded
	i.PostFetchHook(0x0700) // would page out
	if i.Active() {
		t.Errorf("hooks fired without a ROM loaded")
	}
}

func TestResetPagesOut(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}
	i.PageIn()
	if !i.Active() {
		t.Fatal("setup failed: not active")
	}
	i.Reset()
	if i.Active() {
		t.Error("after Reset: still active")
	}
	if !i.HasROM() {
		t.Error("after Reset: ROM lost")
	}
}

func TestLoadROM_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "if1-2.rom")
	rom := make([]byte, ROMSize)
	for i := range rom {
		rom[i] = byte(i)
	}
	if err := os.WriteFile(path, rom, 0o644); err != nil {
		t.Fatal(err)
	}
	i := New()
	if err := i.LoadROM(path); err != nil {
		t.Fatalf("LoadROM: %v", err)
	}
	if !i.HasROM() {
		t.Errorf("after LoadROM: HasROM = false")
	}
	got := i.ROM()
	if len(got) != ROMSize {
		t.Errorf("ROM length = %d, want %d", len(got), ROMSize)
	}
	// Verify content.
	for off := 0; off < ROMSize; off += 1024 {
		if got[off] != byte(off) {
			t.Errorf("ROM[%d] = %02X, want %02X", off, got[off], byte(off))
			break
		}
	}
}

func TestLoadROM_MissingFile(t *testing.T) {
	i := New()
	if err := i.LoadROM("/nonexistent/if1.rom"); err == nil {
		t.Errorf("LoadROM missing file = nil err")
	}
}

func TestLoadROM_BadSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.rom")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	i := New()
	err := i.LoadROM(path)
	if err == nil {
		t.Errorf("LoadROM bad-size = nil err")
	}
}

func TestROMReturnsNilWhenUnloaded(t *testing.T) {
	i := New()
	if got := i.ROM(); got != nil {
		t.Errorf("ROM() on fresh IF1 = non-nil")
	}
}

func TestCartridgeOOBReturnsNil(t *testing.T) {
	i := New()
	// No cartridge loaded → empty slot.
	if got := i.Cartridge(0); got != nil {
		t.Errorf("Cartridge(0) on empty IF1 = non-nil")
	}
	if got := i.Cartridge(-1); got != nil {
		t.Errorf("Cartridge(-1) = non-nil (OOB guard)")
	}
	if got := i.Cartridge(99); got != nil {
		t.Errorf("Cartridge(99) = non-nil (OOB guard)")
	}
}
