package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// mmuTestROMs lays down minimal ROM blobs so memory.New succeeds
// for whatever model the caller asks for. Identical content to the
// existing tests but private to this file so the table-driven cases
// here are self-contained.
func mmuTestROMs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]int{
		"48.rom":      0x4000,
		"128-0.rom":   0x4000,
		"128-1.rom":   0x4000,
		"plus2-0.rom": 0x4000,
		"plus2-1.rom": 0x4000,
		"plus3-0.rom": 0x4000,
		"plus3-1.rom": 0x4000,
		"plus3-2.rom": 0x4000,
		"plus3-3.rom": 0x4000,
	}
	for name, size := range files {
		data := make([]byte, size)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newMemoryForMMUTest(t *testing.T, model roms.SpectrumModel) *Memory {
	t.Helper()
	m, err := New(mmuTestROMs(t), model)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return m
}

// On power-on a 48K Memory has the canonical Next reset layout:
// slot 0,1 = ROM (0xFF), slot 2,3 = bank 5 lo/hi, slot 4,5 = bank 2,
// slot 6,7 = bank 0. No MMU overrides set.
func TestMMUDefaultLayout48K(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.Model48K)
	want := [8]byte{0xFF, 0xFF, 10, 11, 4, 5, 0, 1}
	for i := byte(0); i < 8; i++ {
		if got := m.GetMMU(i); got != want[i] {
			t.Errorf("slot %d: GetMMU = %#x, want %#x", i, got, want[i])
		}
		if m.IsMMUOverridden(i) {
			t.Errorf("slot %d: mmuOverride should be false on power-on", i)
		}
	}
}

func TestMMUSetMarksOverride(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.Model128K)
	m.SetMMU(3, 0x42)
	if m.GetMMU(3) != 0x42 {
		t.Errorf("GetMMU(3) = %#x, want 0x42", m.GetMMU(3))
	}
	if !m.IsMMUOverridden(3) {
		t.Errorf("slot 3: mmuOverride should be set after SetMMU")
	}
	// Other slots untouched.
	for i := byte(0); i < 8; i++ {
		if i == 3 {
			continue
		}
		if m.IsMMUOverridden(i) {
			t.Errorf("slot %d: mmuOverride should not be set", i)
		}
	}
}

func TestMMUClassicPagingUpdatesShadow(t *testing.T) {
	// 7FFD bank-select to bank 4: slots 6 and 7 (8K) should become
	// 8 and 9 (8K banks 8 and 9, which are bank 4 lo and hi).
	m := newMemoryForMMUTest(t, roms.Model128K)
	m.PageMemory(0x04) // bits 0-2 = 4
	if m.GetMMU(6) != 8 {
		t.Errorf("slot 6 after 7FFD=4: %#x, want 8", m.GetMMU(6))
	}
	if m.GetMMU(7) != 9 {
		t.Errorf("slot 7 after 7FFD=4: %#x, want 9", m.GetMMU(7))
	}
	// Slots 0-5 untouched by a 7FFD bits 0-2 change.
	if m.GetMMU(0) != 0xFF || m.GetMMU(2) != 10 {
		t.Errorf("slot 0/2 disturbed by 7FFD=4: slot0=%#x slot2=%#x", m.GetMMU(0), m.GetMMU(2))
	}
}

func TestMMUClassicClearsOverride(t *testing.T) {
	// SetMMU on slot 7 (override), then 7FFD bank-select should clear it.
	m := newMemoryForMMUTest(t, roms.Model128K)
	m.SetMMU(7, 0xAA)
	if !m.IsMMUOverridden(7) {
		t.Fatalf("precondition: slot 7 should be overridden after SetMMU")
	}
	m.PageMemory(0x02) // bank 2 -> slots 6,7 become 4,5
	if m.IsMMUOverridden(7) {
		t.Errorf("slot 7: mmuOverride should be cleared by 7FFD write")
	}
	if m.GetMMU(7) != 5 {
		t.Errorf("slot 7: GetMMU = %#x, want 5 (bank 2 hi)", m.GetMMU(7))
	}
}

func TestMMUClassicOnlyClearsAffectedSlots(t *testing.T) {
	// SetMMU on slot 3 (a slot 7FFD bits 0-2 does NOT touch), then
	// 7FFD bank-select; slot 3's override must survive.
	m := newMemoryForMMUTest(t, roms.Model128K)
	m.SetMMU(3, 0x55)
	m.PageMemory(0x03)
	if !m.IsMMUOverridden(3) {
		t.Errorf("slot 3: mmuOverride should survive a 7FFD bank-select")
	}
	if m.GetMMU(3) != 0x55 {
		t.Errorf("slot 3: GetMMU = %#x, want 0x55", m.GetMMU(3))
	}
}

func TestMMUPlus3SpecialPagingResyncsAll(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelPlus3)
	// First mark every slot as MMU-overridden.
	for i := byte(0); i < 8; i++ {
		m.SetMMU(i, 0x99)
	}
	// Then trigger +3 special paging mode 0 (banks 0,1,2,3) — this
	// must overwrite ALL slots and clear all overrides.
	m.PageMemoryPlus3(0x01) // bit 0 = special, mode = 0
	for i := byte(0); i < 8; i++ {
		if m.IsMMUOverridden(i) {
			t.Errorf("slot %d: special paging should have cleared override", i)
		}
	}
	// Special paging mode 0 = banks 0,1,2,3 -> 8K banks 0,1,2,3,4,5,6,7.
	want := [8]byte{0, 1, 2, 3, 4, 5, 6, 7}
	for i := byte(0); i < 8; i++ {
		if got := m.GetMMU(i); got != want[i] {
			t.Errorf("slot %d after special mode 0: %#x, want %#x", i, got, want[i])
		}
	}
}

func TestMMUResetRestoresDefault(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.Model48K)
	m.SetMMU(4, 0xCC)
	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// After reset, slot 4 should be back to the default (bank 2 lo = 4).
	if m.GetMMU(4) != 4 {
		t.Errorf("slot 4 after Reset: %#x, want 4", m.GetMMU(4))
	}
	if m.IsMMUOverridden(4) {
		t.Errorf("slot 4: mmuOverride should be cleared by Reset")
	}
}

func TestMMUSetOutOfRange(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.Model48K)
	// Slot 8 is out of range; should be a no-op.
	m.SetMMU(8, 0xFF)
	m.SetMMU(255, 0xFF)
	// All real slots still at defaults.
	if m.GetMMU(0) != 0xFF || m.GetMMU(7) != 1 {
		t.Errorf("out-of-range SetMMU disturbed real slots")
	}
}
