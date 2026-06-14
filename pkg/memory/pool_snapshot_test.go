package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestSnapshotRestorePoolRoundTrip verifies the full 2 MB RAM pool can
// be captured and restored byte-for-byte, including banks not currently
// mapped into the CPU window. This underpins time-travel Phase 2b:
// faithful Next rewind needs every pool bank, not just the visible 8.
func TestSnapshotRestorePoolRoundTrip(t *testing.T) {
	m := newMemoryForMMUTest(t, roms.ModelNext)

	// Distinctive byte in a high, non-visible pool bank.
	if b := m.GetPage(50); b != nil {
		b[100] = 0xAB
	} else {
		t.Fatal("pool bank 50 is nil")
	}

	snap := m.SnapshotPool()

	// Mutating live RAM must not change the captured copy.
	m.GetPage(50)[100] = 0x00
	// Restore brings the captured value back.
	m.RestorePool(snap)
	if got := m.GetPage(50)[100]; got != 0xAB {
		t.Errorf("after RestorePool, bank 50 off $64 = $%02X, want $AB", got)
	}
}
