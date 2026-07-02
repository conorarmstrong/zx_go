package z80

import "testing"

// Per-opcode M-cycle reference table — the documented
// machine-cycle decomposition that is the companion to the T-state
// table (Zilog UM008011 §"Instruction Set", Sean Young §A.1).
//
// The CPU models timing at T-state granularity (now per-machine-cycle
// for contention-converted opcodes via m1/rd/wr/exec). M-cycle counts
// are not a separately-exposed counter, so this table encodes the
// canonical (M-cycles, T-states) pair per representative encoding and
// verifies two invariants:
//
//   1. The emulator's measured T-state total matches the documented
//      T for that opcode (cross-checked through StepInstruction).
//   2. The documented decomposition is internally consistent: T >=
//      4*M is impossible to violate for real Z80 timing (each M-cycle
//      is >= 3 T, the M1 fetch is >= 4 T), and the first M-cycle is
//      always the 4-T (or 5-T for prefixed) opcode fetch.
//
// Every distinct M-cycle count (1..6) is represented.

type mcycleCase struct {
	name   string
	bytes  []byte
	setup  func(*CPU, memoryWriter)
	mcyc   int    // documented machine cycles
	tstate uint64 // documented T-states (documented path)
}

func TestMCycle_ReferenceTable(t *testing.T) {
	hl8000 := func(c *CPU, _ memoryWriter) { c.setHL(0x8000) }
	sp := func(c *CPU, _ memoryWriter) { c.SP = 0x8000 }

	cases := []mcycleCase{
		// 1 M-cycle (4 T): simple register ops, fetch-only.
		{"NOP", []byte{0x00}, nil, 1, 4},
		{"LD B,C", []byte{0x41}, nil, 1, 4},
		{"ADD A,B", []byte{0x80}, nil, 1, 4},
		{"INC A", []byte{0x3C}, nil, 1, 4},
		{"EX DE,HL", []byte{0xEB}, nil, 1, 4},
		// 1 M-cycle, 6 T: 16-bit INC/DEC (fetch + 2 internal).
		{"INC HL", []byte{0x23}, nil, 1, 6},
		{"DEC BC", []byte{0x0B}, nil, 1, 6},
		// 2 M-cycles: fetch + one read.
		{"LD B,n", []byte{0x06, 0x12}, nil, 2, 7},
		{"LD A,(HL)", []byte{0x7E}, hl8000, 2, 7},
		{"ADD A,n", []byte{0xC6, 0x05}, nil, 2, 7},
		{"ADD A,(HL)", []byte{0x86}, hl8000, 2, 7},
		// 3 M-cycles.
		{"LD BC,nn", []byte{0x01, 0x34, 0x12}, nil, 3, 10},
		{"LD (HL),n", []byte{0x36, 0x42}, hl8000, 3, 10},
		{"JP nn", []byte{0xC3, 0x00, 0x90}, nil, 3, 10},
		{"INC (HL)", []byte{0x34}, hl8000, 3, 11},
		{"ADD HL,BC", []byte{0x09}, nil, 3, 11},
		{"PUSH BC", []byte{0xC5}, sp, 3, 11},
		{"POP BC", []byte{0xC1}, sp, 3, 10},
		{"RET", []byte{0xC9}, sp, 3, 10},
		{"RST 00", []byte{0xC7}, sp, 3, 11},
		// 4 M-cycles.
		{"LD A,(nn)", []byte{0x3A, 0x00, 0x90}, nil, 4, 13},
		{"LD (nn),A", []byte{0x32, 0x00, 0x90}, nil, 4, 13},
		// 5 M-cycles.
		{"LD (nn),HL", []byte{0x22, 0x00, 0x90}, nil, 5, 16},
		{"LD HL,(nn)", []byte{0x2A, 0x00, 0x90}, nil, 5, 16},
		{"CALL nn", []byte{0xCD, 0x00, 0x90}, sp, 5, 17},
		{"EX (SP),HL", []byte{0xE3}, sp, 5, 19},
		// 6 M-cycles: CALL cc taken is 5; the 6-M-cycle base case is
		// EX (SP),HL on... actually max base is 5/6 via CALL. Use a
		// prefixed op for 6: ED LD (nn),BC = 6 M-cycles, 20 T.
		{"LD (nn),BC", []byte{0xED, 0x43, 0x00, 0x90}, nil, 6, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Invariant 1: emulator T matches documented T.
			got := runOpcodeWithSetup(t, c.bytes, c.setup)
			if got != c.tstate {
				t.Errorf("%s: emulator %d T, documented %d T", c.name, got, c.tstate)
			}
			// Invariant 2: documented decomposition is physically
			// consistent — each M-cycle is >= 3 T and the opcode fetch
			// M-cycle is >= 4 T, so T >= 4 + 3*(M-1).
			min := uint64(4 + 3*(c.mcyc-1))
			if c.tstate < min {
				t.Errorf("%s: documented %d T < physical min %d T for %d M-cycles",
					c.name, c.tstate, min, c.mcyc)
			}
			// And T cannot exceed an all-long-cycle bound (no base Z80
			// M-cycle exceeds 6 T): T <= 6*M + a small internal-cycle
			// allowance. Loose upper sanity bound.
			if c.tstate > uint64(c.mcyc)*7+4 {
				t.Errorf("%s: documented %d T implausibly high for %d M-cycles",
					c.name, c.tstate, c.mcyc)
			}
		})
	}
}
