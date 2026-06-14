package divmmc

import "testing"

// TestSnapshotRestoreRAMRoundTrip verifies the divMMC RAM (the separate
// 16×8K pool, distinct from main RAM) round-trips. Faithful Next
// time-travel rewind (Phase 2b) must restore divMMC RAM too — the SD
// driver's channel/scratch structures live there.
func TestSnapshotRestoreRAMRoundTrip(t *testing.T) {
	p := New([]byte{0xFF})

	p.RAMBank(3)[200] = 0x5A
	snap := p.SnapshotRAM()

	p.RAMBank(3)[200] = 0x00 // disturb
	p.RestoreRAM(snap)

	if got := p.RAMBank(3)[200]; got != 0x5A {
		t.Errorf("divMMC RAM bank 3 off 200 = $%02X after restore, want $5A", got)
	}
}
