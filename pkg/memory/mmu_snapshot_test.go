package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestSnapshotRestoreMMURoundTrip verifies the 8-entry MMU8 slot table
// ($50-$57) round-trips. Faithful Next time-travel rewind (Phase 2b)
// must restore which 8K bank each slot maps, not just the pool bytes.
func TestSnapshotRestoreMMURoundTrip(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)

	for slot := byte(0); slot < 8; slot++ {
		m.SetMMU(slot, byte(0x10+slot))
	}
	snap := m.SnapshotMMU()

	// Disturb the slots, then restore.
	for slot := byte(0); slot < 8; slot++ {
		m.SetMMU(slot, 0xFF)
	}
	m.RestoreMMU(snap)

	for slot := byte(0); slot < 8; slot++ {
		if got := m.GetMMU(slot); got != 0x10+slot {
			t.Errorf("slot %d = $%02X after restore, want $%02X", slot, got, 0x10+slot)
		}
	}
}
