package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestRAM8KPage_BankIndexing pins the 8K-MMU-bank → 16K-backing-page
// mapping that the full-state audit relies on: hardware 8K bank N maps
// to m.ram[N>>1] at offset (N&1)*0x2000 (mirrors readValue at
// memory.go:967). bank0 = page0 low half, bank1 = page0 high half,
// bank2 = page1 low half.
func TestRAM8KPage_BankIndexing(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}

	// Stamp a marker at the first byte of three distinct 8K banks via
	// the backing store directly (same package), then read them back
	// through the accessor.
	m.ram[0][0x0000] = 0xA1 // 8K bank 0 (page 0, low half)
	m.ram[0][0x2000] = 0xB2 // 8K bank 1 (page 0, high half)
	m.ram[1][0x0000] = 0xC3 // 8K bank 2 (page 1, low half)

	cases := []struct {
		bank8k int
		want   byte
	}{
		{0, 0xA1},
		{1, 0xB2},
		{2, 0xC3},
	}
	for _, c := range cases {
		p := m.RAM8KPage(c.bank8k)
		if p == nil {
			t.Fatalf("RAM8KPage(%d) = nil", c.bank8k)
		}
		if len(p) != 0x2000 {
			t.Fatalf("RAM8KPage(%d) len = %#x, want 0x2000", c.bank8k, len(p))
		}
		if p[0] != c.want {
			t.Errorf("RAM8KPage(%d)[0] = $%02X, want $%02X", c.bank8k, p[0], c.want)
		}
	}

	// Out-of-range is nil, not a panic.
	if m.RAM8KPage(100000) != nil {
		t.Errorf("RAM8KPage(out-of-range) should be nil")
	}
}
