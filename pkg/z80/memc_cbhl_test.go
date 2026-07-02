package z80

import "testing"

// CB (HL) bit/shift/res/set memory contention. The (HL)
// read/write of these CB-prefixed RMW ops goes through rd/wr, so a
// contended (HL) incurs position-dependent ULA contention.
func TestMemContention_CBHL(t *testing.T) {
	const displayT = 14336
	const borderT = 100000
	hl := func(v uint16) func(*CPU) { return func(c *CPU) { c.setHL(v) } }
	cases := []struct {
		name      string
		bytes     []byte
		setup     func(*CPU)
		expectGap bool
	}{
		{"RLC (HL) contended", []byte{0xCB, 0x06}, hl(0x4000), true},
		{"SRL (HL) contended", []byte{0xCB, 0x3E}, hl(0x4000), true},
		{"BIT 0,(HL) contended", []byte{0xCB, 0x46}, hl(0x4000), true},
		{"RES 3,(HL) contended", []byte{0xCB, 0x9E}, hl(0x4000), true},
		{"SET 7,(HL) contended", []byte{0xCB, 0xFE}, hl(0x4000), true},
		{"RLC (HL) uncontended", []byte{0xCB, 0x06}, hl(0x8000), false},
		{"BIT 0,(HL) uncontended", []byte{0xCB, 0x46}, hl(0x8000), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, displayT, c.setup)
			outside := ioContentionCost(t, c.bytes, borderT, c.setup)
			if c.expectGap && inside <= outside {
				t.Errorf("%s: %d not > %d", c.name, inside, outside)
			}
			if !c.expectGap && inside != outside {
				t.Errorf("%s: %d != %d (spurious)", c.name, inside, outside)
			}
		})
	}
}

// Totals preserved: BIT n,(HL)=12, shift/RES/SET (HL)=15.
func TestMemContention_CBHLTotals(t *testing.T) {
	hl := func(c *CPU) { c.setHL(0x8000) }
	cases := []struct {
		name  string
		bytes []byte
		want  uint64
	}{
		{"RLC (HL)", []byte{0xCB, 0x06}, 15},
		{"SRL (HL)", []byte{0xCB, 0x3E}, 15},
		{"BIT 0,(HL)", []byte{0xCB, 0x46}, 12},
		{"RES 0,(HL)", []byte{0xCB, 0x86}, 15},
		{"SET 0,(HL)", []byte{0xCB, 0xC6}, 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ioContentionCost(t, c.bytes, 0, hl)
			if got != c.want {
				t.Errorf("%s: %d T, want %d", c.name, got, c.want)
			}
		})
	}
}
