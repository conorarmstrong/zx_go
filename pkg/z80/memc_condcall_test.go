package z80

import "testing"

// Conditional CALL cc / RET cc stack contention (taken path).
// On the taken path these push/pop through pushC/popC, so a stack in
// contended RAM incurs position-dependent ULA contention.
func TestMemContention_CondCallRet(t *testing.T) {
	const displayT = 14336
	const borderT = 100000
	// CALL NZ taken: Z=0. RET NZ taken: Z=0. Clear Z.
	clrZ := func(sp uint16) func(*CPU) {
		return func(c *CPU) { c.F &^= FLAG_Z; c.SP = sp }
	}
	cases := []struct {
		name      string
		bytes     []byte
		setup     func(*CPU)
		expectGap bool
	}{
		{"CALL NZ taken contended", []byte{0xC4, 0x00, 0x90}, clrZ(0x4002), true},
		{"RET NZ taken contended", []byte{0xC0}, clrZ(0x4000), true},
		{"CALL NZ taken uncontended", []byte{0xC4, 0x00, 0x90}, clrZ(0x8002), false},
		{"RET NZ taken uncontended", []byte{0xC0}, clrZ(0x8000), false},
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

// Both taken/not-taken totals preserved.
func TestMemContention_CondCallRetTotals(t *testing.T) {
	clrZ := func(c *CPU) { c.F &^= FLAG_Z; c.SP = 0x8000 }
	setZ := func(c *CPU) { c.F |= FLAG_Z; c.SP = 0x8000 }
	cases := []struct {
		name  string
		bytes []byte
		setup func(*CPU)
		want  uint64
	}{
		{"CALL NZ taken", []byte{0xC4, 0x00, 0x90}, clrZ, 17},
		{"CALL NZ not-taken", []byte{0xC4, 0x00, 0x90}, setZ, 10},
		{"RET NZ taken", []byte{0xC0}, clrZ, 11},
		{"RET NZ not-taken", []byte{0xC0}, setZ, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ioContentionCost(t, c.bytes, 0, c.setup)
			if got != c.want {
				t.Errorf("%s: %d T, want %d", c.name, got, c.want)
			}
		})
	}
}
