package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// iter 358: NR$50-$57 MMU8 per-value bank-mapping — systematic
// 256-value × 8-slot coverage. SetMMU(slot, page) is the engine the
// NextReg $50-$57 OnWrite handlers drive; this verifies every 8K-page
// value round-trips per slot and that distinct pages mapped into a
// slot expose physically distinct memory.

func TestMMU8_AllValuesRoundTripPerSlot(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	for slot := byte(0); slot < 8; slot++ {
		for page := 0; page <= 0xFF; page++ {
			m.SetMMU(slot, byte(page))
			if got := m.GetMMU(slot); got != byte(page) {
				t.Fatalf("slot %d page $%02X: GetMMU=$%02X, want $%02X", slot, page, got, page)
			}
			if !m.IsMMUOverridden(slot) {
				t.Fatalf("slot %d: override flag not set after SetMMU", slot)
			}
		}
	}
}

// TestMMU8_DistinctPagesDistinctMemory proves the per-value mapping
// actually routes to different physical 8K pages: a byte written
// through a slot mapped to page A survives a detour to page B and
// back. Uses RAM slots 6/7 (default banks 0/1) and high RAM pages so
// the test never lands on ROM or the screen.
func TestMMU8_DistinctPagesDistinctMemory(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	const slot = 6 // $C000-$DFFF
	base := uint16(0x2000 * slot)
	pageA, pageB := byte(0x20), byte(0x21) // distinct high RAM 8K pages

	m.SetMMU(slot, pageA)
	m.Write(base, 0xAA)
	m.SetMMU(slot, pageB)
	m.Write(base, 0xBB)
	// Page B holds 0xBB.
	if got := m.Read(base); got != 0xBB {
		t.Fatalf("page B read = $%02X, want $BB", got)
	}
	// Switch back to page A — must still hold 0xAA (distinct backing).
	m.SetMMU(slot, pageA)
	if got := m.Read(base); got != 0xAA {
		t.Fatalf("page A read after detour = $%02X, want $AA (pages not distinct)", got)
	}
}

// TestMMU8_SlotsIndependent confirms each slot's mapping is
// independent — setting one doesn't perturb another's page.
func TestMMU8_SlotsIndependent(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)
	for slot := byte(0); slot < 8; slot++ {
		m.SetMMU(slot, 0x10+slot)
	}
	for slot := byte(0); slot < 8; slot++ {
		if got := m.GetMMU(slot); got != 0x10+slot {
			t.Errorf("slot %d: GetMMU=$%02X, want $%02X (cross-slot perturbation)", slot, got, 0x10+slot)
		}
	}
}
