package z80

import "testing"

// DD/FD (IX+d)/(IY+d) memory contention. The data access at
// the effective address is routed through rd/wr, so a contended
// target incurs position-dependent ULA contention.
func TestMemContention_DDFDIXd(t *testing.T) {
	const borderT = 100000
	ix := func(v uint16) func(*CPU) { return func(c *CPU) { c.IX = v } }
	iy := func(v uint16) func(*CPU) { return func(c *CPU) { c.IY = v } }
	cases := []struct {
		name      string
		bytes     []byte // d = 0x00
		setup     func(*CPU)
		display   uint64
		expectGap bool
	}{
		{"LD B,(IX+0) contended", []byte{0xDD, 0x46, 0x00}, ix(0x4000), 14336, true},
		{"LD (IX+0),B contended", []byte{0xDD, 0x70, 0x00}, ix(0x4000), 14336, true},
		{"INC (IX+0) contended", []byte{0xDD, 0x34, 0x00}, ix(0x4000), 14336, true},
		{"ADD A,(IX+0) contended", []byte{0xDD, 0x86, 0x00}, ix(0x4000), 14336, true},
		{"LD (IX+0),n contended", []byte{0xDD, 0x36, 0x00, 0x42}, ix(0x4000), 14336, true},
		{"LD C,(IY+0) contended", []byte{0xFD, 0x4E, 0x00}, iy(0x4000), 14336, true},
		{"LD B,(IX+0) uncontended", []byte{0xDD, 0x46, 0x00}, ix(0x8000), 14336, false},
		{"INC (IX+0) uncontended", []byte{0xDD, 0x34, 0x00}, ix(0x8000), 14336, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, c.display, c.setup)
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

// Totals preserved: LD r,(IX+d)=19, LD (IX+d),r=19, INC/DEC (IX+d)=23,
// ALU A,(IX+d)=19, LD (IX+d),n=19.
func TestMemContention_DDFDIXdTotals(t *testing.T) {
	ix := func(c *CPU) { c.IX = 0x8000 }
	cases := []struct {
		name  string
		bytes []byte
		want  uint64
	}{
		{"LD B,(IX+0)", []byte{0xDD, 0x46, 0x00}, 19},
		{"LD (IX+0),B", []byte{0xDD, 0x70, 0x00}, 19},
		{"INC (IX+0)", []byte{0xDD, 0x34, 0x00}, 23},
		{"DEC (IX+0)", []byte{0xDD, 0x35, 0x00}, 23},
		{"ADD A,(IX+0)", []byte{0xDD, 0x86, 0x00}, 19},
		{"LD (IX+0),n", []byte{0xDD, 0x36, 0x00, 0x42}, 19},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ioContentionCost(t, c.bytes, 0, ix)
			if got != c.want {
				t.Errorf("%s: %d T, want %d", c.name, got, c.want)
			}
		})
	}
}
