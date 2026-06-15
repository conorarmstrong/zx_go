package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Pentagon is a 128K clone: bank 0 = Pentagon editor, bank 1 = 48 BASIC,
// 7FFD paging enabled, and (the defining trait) no memory contention.
func TestSetupPentagon(t *testing.T) {
	m, err := New("", roms.ModelPentagon)
	if err != nil {
		t.Fatalf("New(ModelPentagon): %v", err)
	}
	if !m.PagingEnabled {
		t.Error("Pentagon should have 128K paging enabled")
	}
	if m.ScreenPage != 5 {
		t.Errorf("ScreenPage = %d, want 5", m.ScreenPage)
	}
	// Bank 0 is the Pentagon editor (distinct from the 128K editor); bank 1 is
	// the standard 48 BASIC. ROM is read-only.
	rom0 := m.Read(0x0000)
	m.Write(0x0000, rom0^0xFF)
	if m.Read(0x0000) != rom0 {
		t.Error("ROM must be read-only")
	}
	// No contention: even once a T-state pointer is wired, contention stays off.
	var ts uint64
	m.SetTStatePtr(&ts)
	if m.ContentionEnabled {
		t.Error("Pentagon must have contention disabled")
	}
}
