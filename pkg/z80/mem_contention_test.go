package z80

import "testing"

// Per-opcode MEMORY contention. Converted opcodes route
// their memory accesses through the m1/rd/wr/exec cycle helpers, which
// apply ULA contention at each access. An opcode whose (HL) data
// access lands in contended screen RAM ($4000-$7FFF) must cost MORE
// T-states inside the contended display window than outside it; an
// access to uncontended RAM must cost the same in both positions.
//
// Reuses ioContentionCost (io_contention_test.go) to position the CPU
// at a display vs border T-state and measure StepInstruction cost.

func TestMemContention_HLGroup(t *testing.T) {
	// Start one T into the window so every converted op's contended
	// access (M1 fetch is at uncontended PC $8000) lands on a
	// non-zero slot of the {6,5,4,3,2,1,0,0} pattern rather than one
	// of the two trailing zero slots.
	const displayT = 14336
	const borderT = 100000 // past display → no contention

	contended := func(c *CPU) { c.setHL(0x4000) }   // screen RAM
	uncontended := func(c *CPU) { c.setHL(0x8000) } // bank 2, not contended

	cases := []struct {
		name      string
		bytes     []byte
		setup     func(*CPU)
		expectGap bool // true: in-display must exceed border
	}{
		{"LD A,(HL) contended", []byte{0x7E}, contended, true},
		{"LD (HL),A contended", []byte{0x77}, contended, true},
		{"INC (HL) contended", []byte{0x34}, contended, true},
		{"DEC (HL) contended", []byte{0x35}, contended, true},
		{"ADD A,(HL) contended", []byte{0x86}, contended, true},
		{"LD (HL),n contended", []byte{0x36, 0x42}, contended, true},
		{"LD A,(HL) uncontended", []byte{0x7E}, uncontended, false},
		{"INC (HL) uncontended", []byte{0x34}, uncontended, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, displayT, c.setup)
			outside := ioContentionCost(t, c.bytes, borderT, c.setup)
			if c.expectGap && inside <= outside {
				t.Errorf("%s: in-display=%d T not > border=%d T — memory contention not applied",
					c.name, inside, outside)
			}
			if !c.expectGap && inside != outside {
				t.Errorf("%s: in-display=%d T != border=%d T — spurious contention on uncontended addr",
					c.name, inside, outside)
			}
		})
	}
}

// TestMemContention_BaseTotalsUnchanged confirms the converted (HL)
// opcodes still charge their canonical totals when uncontended
// (T-state 0 → contentionDelay 0): LD r,(HL)=7, LD (HL),r=7,
// LD (HL),n=10, INC/DEC (HL)=11, ALU A,(HL)=7. This guards against
// the per-cycle rewrite drifting any base total.
func TestMemContention_BaseTotalsUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		setup func(*CPU)
		want  uint64
	}{
		{"LD A,(HL)", []byte{0x7E}, func(c *CPU) { c.setHL(0x8000) }, 7},
		{"LD (HL),A", []byte{0x77}, func(c *CPU) { c.setHL(0x8000) }, 7},
		{"LD (HL),n", []byte{0x36, 0x42}, func(c *CPU) { c.setHL(0x8000) }, 10},
		{"INC (HL)", []byte{0x34}, func(c *CPU) { c.setHL(0x8000) }, 11},
		{"DEC (HL)", []byte{0x35}, func(c *CPU) { c.setHL(0x8000) }, 11},
		{"ADD A,(HL)", []byte{0x86}, func(c *CPU) { c.setHL(0x8000) }, 7},
		{"AND (HL)", []byte{0xA6}, func(c *CPU) { c.setHL(0x8000) }, 7},
		{"CP (HL)", []byte{0xBE}, func(c *CPU) { c.setHL(0x8000) }, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ioContentionCost(t, c.bytes, 0, c.setup)
			if got != c.want {
				t.Errorf("%s: %d T, want %d T", c.name, got, c.want)
			}
		})
	}
}

// Stack-group memory contention. PUSH/POP/CALL/RET/RST/
// EX(SP),HL route their stack accesses through pushC/popC (contended
// wr/rd), so a stack in contended RAM ($4000-$7FFF) incurs
// position-dependent contention.
func TestMemContention_StackGroup(t *testing.T) {
	const displayT = 14336
	const borderT = 100000

	spLow := func(sp uint16) func(*CPU) { return func(c *CPU) { c.SP = sp } }

	cases := []struct {
		name      string
		bytes     []byte
		setup     func(*CPU)
		expectGap bool
	}{
		{"PUSH BC contended", []byte{0xC5}, spLow(0x4002), true},
		{"POP BC contended", []byte{0xC1}, spLow(0x4000), true},
		{"CALL nn contended", []byte{0xCD, 0x00, 0x90}, spLow(0x4002), true},
		{"RET contended", []byte{0xC9}, spLow(0x4000), true},
		{"RST 00 contended", []byte{0xC7}, spLow(0x4002), true},
		{"EX (SP),HL contended", []byte{0xE3}, spLow(0x4000), true},
		{"PUSH BC uncontended", []byte{0xC5}, spLow(0x8002), false},
		{"RET uncontended", []byte{0xC9}, spLow(0x8000), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, displayT, c.setup)
			outside := ioContentionCost(t, c.bytes, borderT, c.setup)
			if c.expectGap && inside <= outside {
				t.Errorf("%s: in-display=%d not > border=%d — stack contention missing", c.name, inside, outside)
			}
			if !c.expectGap && inside != outside {
				t.Errorf("%s: in-display=%d != border=%d — spurious contention", c.name, inside, outside)
			}
		})
	}
}

func TestMemContention_StackTotalsUnchanged(t *testing.T) {
	sp := func(c *CPU) { c.SP = 0x8000 }
	cases := []struct {
		name  string
		bytes []byte
		want  uint64
	}{
		{"PUSH BC", []byte{0xC5}, 11},
		{"POP BC", []byte{0xC1}, 10},
		{"CALL nn", []byte{0xCD, 0x00, 0x90}, 17},
		{"RET", []byte{0xC9}, 10},
		{"RST 00", []byte{0xC7}, 11},
		{"EX (SP),HL", []byte{0xE3}, 19},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ioContentionCost(t, c.bytes, 0, sp)
			if got != c.want {
				t.Errorf("%s: %d T, want %d T", c.name, got, c.want)
			}
		})
	}
}

// 16-bit absolute memory LD group contention.
// LD (nn),HL / LD HL,(nn) / LD (nn),A / LD A,(nn) route their data
// accesses through rd/wr, so a target address in contended RAM
// incurs position-dependent ULA contention.
func TestMemContention_Abs16Group(t *testing.T) {
	const displayT = 14336
	const borderT = 100000
	cases := []struct {
		name      string
		bytes     []byte // operand nn = $4000 (contended) or $8000
		expectGap bool
	}{
		{"LD HL,(4000)", []byte{0x2A, 0x00, 0x40}, true},
		{"LD (4000),HL", []byte{0x22, 0x00, 0x40}, true},
		{"LD A,(4000)", []byte{0x3A, 0x00, 0x40}, true},
		{"LD (4000),A", []byte{0x32, 0x00, 0x40}, true},
		{"LD A,(8000) uncontended", []byte{0x3A, 0x00, 0x80}, false},
		{"LD (8000),HL uncontended", []byte{0x22, 0x00, 0x80}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, displayT, nil)
			outside := ioContentionCost(t, c.bytes, borderT, nil)
			if c.expectGap && inside <= outside {
				t.Errorf("%s: in-display=%d not > border=%d", c.name, inside, outside)
			}
			if !c.expectGap && inside != outside {
				t.Errorf("%s: in-display=%d != border=%d (spurious)", c.name, inside, outside)
			}
		})
	}
}

// LD A,(BC)/(DE) + LD (BC)/(DE),A indirect group contention.
func TestMemContention_IndirectBCDE(t *testing.T) {
	const displayT = 14336
	const borderT = 100000
	bc := func(v uint16) func(*CPU) { return func(c *CPU) { c.setBC(v) } }
	de := func(v uint16) func(*CPU) { return func(c *CPU) { c.setDE(v) } }
	cases := []struct {
		name      string
		bytes     []byte
		setup     func(*CPU)
		expectGap bool
	}{
		{"LD A,(BC) contended", []byte{0x0A}, bc(0x4000), true},
		{"LD (BC),A contended", []byte{0x02}, bc(0x4000), true},
		{"LD A,(DE) contended", []byte{0x1A}, de(0x4000), true},
		{"LD (DE),A contended", []byte{0x12}, de(0x4000), true},
		{"LD A,(BC) uncontended", []byte{0x0A}, bc(0x8000), false},
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
