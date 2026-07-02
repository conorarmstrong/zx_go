package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// TestApplyDirectBootNextRegs verifies the core-config personality is
// seeded so NextZXOS init reads the machine it expects (the CPU-speed
// read at $0991 returns 28 MHz, divMMC automap on, etc.).
func TestApplyDirectBootNextRegs(t *testing.T) {
	d := nextregs.New()
	applyDirectBootNextRegs(d)

	cases := map[byte]byte{
		0x03: 0x33, // machine/timing
		0x07: 0x33, // CPU speed (read at $0991)
		0x08: 0xDE, // peripheral enables
		0xB8: 0x82, // divMMC entry-points
		0xC0: 0x08, // interrupt control
	}
	for reg, want := range cases {
		if got := d.Raw(reg); got != want {
			t.Errorf("NR$%02X = $%02X, want $%02X", reg, got, want)
		}
	}
	// divMMC automap (NR$0A bit4) seeded on at reset.
	if d.Raw(0x0A)&0x10 == 0 {
		t.Errorf("NR$0A=$%02X — automap bit should be set", d.Raw(0x0A))
	}
	// nil is a no-op (must not panic)
	applyDirectBootNextRegs(nil)
}
