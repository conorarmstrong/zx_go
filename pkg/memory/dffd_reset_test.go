package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestDFFDClearedOnReset verifies that $DFFD (the high RAM-bank
// extension for the $C000 slot) is cleared by Reset(), matching
// zxnext.vhd's port_dffd_reg reset block — the same `reset` signal
// that clears port_7ffd_reg and port_1ffd_reg also clears
// port_dffd_reg. Without this, a stale DFFD value from before the
// reset silently extends the bank computed by the next $7FFD write.
func TestDFFDClearedOnReset(t *testing.T) {
	m, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.SetDFFD(0x01)
	if got := m.DFFDValue(); got != 0x01 {
		t.Fatalf("DFFDValue after SetDFFD(0x01) = %#x, want 0x01", got)
	}
	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := m.DFFDValue(); got != 0 {
		t.Errorf("DFFDValue after Reset = %#x, want 0", got)
	}
	// A subsequent classic 7FFD write must compute the RAM bank from
	// the now-zeroed DFFD extension, not the stale pre-reset value.
	m.PageMemory(0x00)
	if got := m.ResolvePage(0xC000); got != 0 {
		t.Errorf("ResolvePage($C000) after Reset+7FFD(0) = %d, want 0", got)
	}
}

// TestDFFDClearedOnResetPaging is the Next NR$02 soft-reset analogue
// of TestDFFDClearedOnReset.
func TestDFFDClearedOnResetPaging(t *testing.T) {
	m, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.SetDFFD(0x01)
	m.ResetPaging()
	if got := m.DFFDValue(); got != 0 {
		t.Errorf("DFFDValue after ResetPaging = %#x, want 0", got)
	}
	m.PageMemory(0x00)
	if got := m.ResolvePage(0xC000); got != 0 {
		t.Errorf("ResolvePage($C000) after ResetPaging+7FFD(0) = %d, want 0", got)
	}
}

// TestDFFDIgnoredWhilePagingLocked verifies that a $DFFD write while
// paging is locked (7FFD bit 5) is dropped, matching zxnext.vhd:
// port_dffd_reg only latches when `port_dffd_wr='1' and
// port_7ffd_locked='0'`.
func TestDFFDIgnoredWhilePagingLocked(t *testing.T) {
	m, err := New(mmuTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.PageMemory(0x20) // bit 5 set = lock paging
	m.SetDFFD(0x0F)
	if got := m.DFFDValue(); got != 0 {
		t.Errorf("DFFDValue after SetDFFD while locked = %#x, want 0 (write must be dropped)", got)
	}
}
