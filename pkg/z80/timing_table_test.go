package z80

import (
	"fmt"
	"testing"
)

// Canonical Z80 base-opcode T-state table.
//
// One entry per encoding. Sources: Sean Young, "Undocumented Z80
// Documented" §A.1 (timing tables) and Zilog UM008011-0816. Values
// match the documented "M-cycle x T-state" totals for the documented
// path. For conditional opcodes (JR cc, JP cc, CALL cc, RET cc, DJNZ)
// the table records the NOT-taken cost; companion taken-path tests
// are already in z80_test.go.
//
// Coverage convention:
//   - All 256 base opcodes are listed by group.
//   - HALT (0x76) is special-cased: our StepInstruction model burns
//     4 t per call once Halted=true, matching the M1+NOP repeat
//     real hardware does while waiting for INT.
//   - 0xCB/0xDD/0xED/0xFD prefix bytes are excluded — their inner
//     opcodes are covered by timing_table_cb_test / ed / dd / fd /
//     ddcb / fdcb suites.

// baseTimingCase encodes one opcode-encoding test. setup is run on
// the CPU before execution; it may set flags / registers / memory.
type baseTimingCase struct {
	name  string
	bytes []byte
	setup func(c *CPU, m memoryWriter)
	want  uint64
}

type memoryWriter interface {
	Write(addr uint16, val byte)
}

// runOpcodeWithSetup is like runOpcode but lets the test prime the
// CPU/memory state first.
func runOpcodeWithSetup(t *testing.T, bytes []byte, setup func(*CPU, memoryWriter)) uint64 {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	for i, b := range bytes {
		mem.Write(uint16(0x8000+i), b)
	}
	if setup != nil {
		setup(cpu, mem)
	}
	before := cpu.tstates
	cpu.StepInstruction()
	return cpu.tstates - before
}

// setFlagsClear primes flags so all conditional jumps with negated
// flag conditions (NZ, NC, PO, P) take the FALL-THROUGH path. With
// F=0:
//
//	NZ → Z=0 → taken (so for "not taken" need Z=1)
//	Z  → Z=0 → not taken
//	NC → C=0 → taken (so for "not taken" need C=1)
//	C  → C=0 → not taken
//	PO → PV=0 → taken (so for "not taken" need PV=1)
//	PE → PV=0 → not taken
//	P  → S=0 → taken (so for "not taken" need S=1)
//	M  → S=0 → not taken
//
// To unify "not taken" across all 8 conditions we set F = Z|C|PV|S
// = $C5. Then:
//
//	NZ → not taken; Z → taken
//	NC → not taken; C → taken
//	PO → not taken; PE → taken
//	P  → not taken; M → taken
//
// Pick one configuration per cc. We use the FALSE-of-flag (not taken)
// path for tests labelled "_nt" and the TRUE path for "_t" suffixes.
func setFlagsNotTakenFor(cc int) func(c *CPU, _ memoryWriter) {
	// cc index 0..7 corresponds to NZ, Z, NC, C, PO, PE, P, M (Z80
	// encoding order).
	return func(c *CPU, _ memoryWriter) {
		switch cc {
		case 0: // NZ — needs Z=1 to NOT take
			c.F |= FLAG_Z
		case 1: // Z — needs Z=0 to NOT take
			c.F &^= FLAG_Z
		case 2: // NC — needs C=1
			c.F |= FLAG_C
		case 3: // C — needs C=0
			c.F &^= FLAG_C
		case 4: // PO — needs PV=1
			c.F |= FLAG_PV
		case 5: // PE — needs PV=0
			c.F &^= FLAG_PV
		case 6: // P — needs S=1
			c.F |= FLAG_S
		case 7: // M — needs S=0
			c.F &^= FLAG_S
		}
	}
}

// TestTimingTable_BaseOpcodes is the canonical truth-table for every
// base opcode T-state count. Some entries duplicate coverage from
// older targeted tests; the duplication is intentional — this table
// is the single place future readers find the canonical timing.
func TestTimingTable_BaseOpcodes(t *testing.T) {
	cases := []baseTimingCase{
		// $00..$0F
		{"NOP", []byte{0x00}, nil, 4},
		{"LD BC,nn", []byte{0x01, 0x34, 0x12}, nil, 10},
		{"LD (BC),A", []byte{0x02}, nil, 7},
		{"INC BC", []byte{0x03}, nil, 6},
		{"INC B", []byte{0x04}, nil, 4},
		{"DEC B", []byte{0x05}, nil, 4},
		{"LD B,n", []byte{0x06, 0x42}, nil, 7},
		{"RLCA", []byte{0x07}, nil, 4},
		{"EX AF,AF'", []byte{0x08}, nil, 4},
		{"ADD HL,BC", []byte{0x09}, nil, 11},
		{"LD A,(BC)", []byte{0x0A}, nil, 7},
		{"DEC BC", []byte{0x0B}, nil, 6},
		{"INC C", []byte{0x0C}, nil, 4},
		{"DEC C", []byte{0x0D}, nil, 4},
		{"LD C,n", []byte{0x0E, 0x42}, nil, 7},
		{"RRCA", []byte{0x0F}, nil, 4},
		// $10..$1F — 0x10 DJNZ not taken: B-- = 0
		{"DJNZ d (not taken)", []byte{0x10, 0x05}, func(c *CPU, _ memoryWriter) { c.B = 1 }, 8},
		{"LD DE,nn", []byte{0x11, 0x34, 0x12}, nil, 10},
		{"LD (DE),A", []byte{0x12}, nil, 7},
		{"INC DE", []byte{0x13}, nil, 6},
		{"INC D", []byte{0x14}, nil, 4},
		{"DEC D", []byte{0x15}, nil, 4},
		{"LD D,n", []byte{0x16, 0x42}, nil, 7},
		{"RLA", []byte{0x17}, nil, 4},
		{"JR d", []byte{0x18, 0x05}, nil, 12},
		{"ADD HL,DE", []byte{0x19}, nil, 11},
		{"LD A,(DE)", []byte{0x1A}, nil, 7},
		{"DEC DE", []byte{0x1B}, nil, 6},
		{"INC E", []byte{0x1C}, nil, 4},
		{"DEC E", []byte{0x1D}, nil, 4},
		{"LD E,n", []byte{0x1E, 0x42}, nil, 7},
		{"RRA", []byte{0x1F}, nil, 4},
		// $20..$2F — JR cc not taken = 7
		{"JR NZ,d (not taken)", []byte{0x20, 0x05}, setFlagsNotTakenFor(0), 7},
		{"LD HL,nn", []byte{0x21, 0x34, 0x12}, nil, 10},
		{"LD (nn),HL", []byte{0x22, 0x00, 0x90}, nil, 16},
		{"INC HL", []byte{0x23}, nil, 6},
		{"INC H", []byte{0x24}, nil, 4},
		{"DEC H", []byte{0x25}, nil, 4},
		{"LD H,n", []byte{0x26, 0x42}, nil, 7},
		{"DAA", []byte{0x27}, nil, 4},
		{"JR Z,d (not taken)", []byte{0x28, 0x05}, setFlagsNotTakenFor(1), 7},
		{"ADD HL,HL", []byte{0x29}, nil, 11},
		{"LD HL,(nn)", []byte{0x2A, 0x00, 0x90}, nil, 16},
		{"DEC HL", []byte{0x2B}, nil, 6},
		{"INC L", []byte{0x2C}, nil, 4},
		{"DEC L", []byte{0x2D}, nil, 4},
		{"LD L,n", []byte{0x2E, 0x42}, nil, 7},
		{"CPL", []byte{0x2F}, nil, 4},
		// $30..$3F
		{"JR NC,d (not taken)", []byte{0x30, 0x05}, setFlagsNotTakenFor(2), 7},
		{"LD SP,nn", []byte{0x31, 0x34, 0x12}, nil, 10},
		{"LD (nn),A", []byte{0x32, 0x00, 0x90}, nil, 13},
		{"INC SP", []byte{0x33}, nil, 6},
		{"INC (HL)", []byte{0x34}, func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }, 11},
		{"DEC (HL)", []byte{0x35}, func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }, 11},
		{"LD (HL),n", []byte{0x36, 0x42}, func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }, 10},
		{"SCF", []byte{0x37}, nil, 4},
		{"JR C,d (not taken)", []byte{0x38, 0x05}, setFlagsNotTakenFor(3), 7},
		{"ADD HL,SP", []byte{0x39}, nil, 11},
		{"LD A,(nn)", []byte{0x3A, 0x00, 0x90}, nil, 13},
		{"DEC SP", []byte{0x3B}, nil, 6},
		{"INC A", []byte{0x3C}, nil, 4},
		{"DEC A", []byte{0x3D}, nil, 4},
		{"LD A,n", []byte{0x3E, 0x42}, nil, 7},
		{"CCF", []byte{0x3F}, nil, 4},
	}
	// $40..$7F LD r,r' / LD r,(HL) / LD (HL),r / HALT.
	for op := 0x40; op <= 0x7F; op++ {
		dst := (op >> 3) & 7
		src := op & 7
		var want uint64
		var setup func(c *CPU, _ memoryWriter)
		var name string
		if op == 0x76 {
			name = "HALT"
			want = 4
		} else if dst == 6 || src == 6 {
			// touches (HL)
			name = fmt.Sprintf("LD %s,%s", regName(dst), regName(src))
			want = 7
			setup = func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }
		} else {
			name = fmt.Sprintf("LD %s,%s", regName(dst), regName(src))
			want = 4
		}
		cases = append(cases, baseTimingCase{name, []byte{byte(op)}, setup, want})
	}
	// $80..$BF ALU A,r / A,(HL).
	for op := 0x80; op <= 0xBF; op++ {
		src := op & 7
		opcode := (op >> 3) & 7
		var want uint64
		var setup func(c *CPU, _ memoryWriter)
		if src == 6 {
			want = 7
			setup = func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }
		} else {
			want = 4
		}
		name := fmt.Sprintf("%s A,%s", aluName(opcode), regName(src))
		cases = append(cases, baseTimingCase{name, []byte{byte(op)}, setup, want})
	}
	// $C0..$FF
	moreCases := []baseTimingCase{
		{"RET NZ (not taken)", []byte{0xC0}, setFlagsNotTakenFor(0), 5},
		{"POP BC", []byte{0xC1}, nil, 10},
		{"JP NZ,nn (not taken)", []byte{0xC2, 0x00, 0x90}, setFlagsNotTakenFor(0), 10},
		{"JP nn", []byte{0xC3, 0x00, 0x90}, nil, 10},
		{"CALL NZ,nn (not taken)", []byte{0xC4, 0x00, 0x90}, setFlagsNotTakenFor(0), 10},
		{"PUSH BC", []byte{0xC5}, nil, 11},
		{"ADD A,n", []byte{0xC6, 0x42}, nil, 7},
		{"RST 00", []byte{0xC7}, nil, 11},
		{"RET Z (not taken)", []byte{0xC8}, setFlagsNotTakenFor(1), 5},
		{"RET", []byte{0xC9}, nil, 10},
		{"JP Z,nn (not taken)", []byte{0xCA, 0x00, 0x90}, setFlagsNotTakenFor(1), 10},
		// 0xCB prefix — skipped (CB-prefix opcodes covered elsewhere)
		{"CALL Z,nn (not taken)", []byte{0xCC, 0x00, 0x90}, setFlagsNotTakenFor(1), 10},
		{"CALL nn", []byte{0xCD, 0x00, 0x90}, nil, 17},
		{"ADC A,n", []byte{0xCE, 0x42}, nil, 7},
		{"RST 08", []byte{0xCF}, nil, 11},
		{"RET NC (not taken)", []byte{0xD0}, setFlagsNotTakenFor(2), 5},
		{"POP DE", []byte{0xD1}, nil, 10},
		{"JP NC,nn (not taken)", []byte{0xD2, 0x00, 0x90}, setFlagsNotTakenFor(2), 10},
		{"OUT (n),A", []byte{0xD3, 0x55}, nil, 11},
		{"CALL NC,nn (not taken)", []byte{0xD4, 0x00, 0x90}, setFlagsNotTakenFor(2), 10},
		{"PUSH DE", []byte{0xD5}, nil, 11},
		{"SUB n", []byte{0xD6, 0x42}, nil, 7},
		{"RST 10", []byte{0xD7}, nil, 11},
		{"RET C (not taken)", []byte{0xD8}, setFlagsNotTakenFor(3), 5},
		{"EXX", []byte{0xD9}, nil, 4},
		{"JP C,nn (not taken)", []byte{0xDA, 0x00, 0x90}, setFlagsNotTakenFor(3), 10},
		{"IN A,(n)", []byte{0xDB, 0x55}, nil, 11},
		{"CALL C,nn (not taken)", []byte{0xDC, 0x00, 0x90}, setFlagsNotTakenFor(3), 10},
		// 0xDD prefix — skipped
		{"SBC A,n", []byte{0xDE, 0x42}, nil, 7},
		{"RST 18", []byte{0xDF}, nil, 11},
		{"RET PO (not taken)", []byte{0xE0}, setFlagsNotTakenFor(4), 5},
		{"POP HL", []byte{0xE1}, nil, 10},
		{"JP PO,nn (not taken)", []byte{0xE2, 0x00, 0x90}, setFlagsNotTakenFor(4), 10},
		{"EX (SP),HL", []byte{0xE3}, nil, 19},
		{"CALL PO,nn (not taken)", []byte{0xE4, 0x00, 0x90}, setFlagsNotTakenFor(4), 10},
		{"PUSH HL", []byte{0xE5}, nil, 11},
		{"AND n", []byte{0xE6, 0x42}, nil, 7},
		{"RST 20", []byte{0xE7}, nil, 11},
		{"RET PE (not taken)", []byte{0xE8}, setFlagsNotTakenFor(5), 5},
		{"JP (HL)", []byte{0xE9}, nil, 4},
		{"JP PE,nn (not taken)", []byte{0xEA, 0x00, 0x90}, setFlagsNotTakenFor(5), 10},
		{"EX DE,HL", []byte{0xEB}, nil, 4},
		{"CALL PE,nn (not taken)", []byte{0xEC, 0x00, 0x90}, setFlagsNotTakenFor(5), 10},
		// 0xED prefix — skipped
		{"XOR n", []byte{0xEE, 0x42}, nil, 7},
		{"RST 28", []byte{0xEF}, nil, 11},
		{"RET P (not taken)", []byte{0xF0}, setFlagsNotTakenFor(6), 5},
		{"POP AF", []byte{0xF1}, nil, 10},
		{"JP P,nn (not taken)", []byte{0xF2, 0x00, 0x90}, setFlagsNotTakenFor(6), 10},
		{"DI", []byte{0xF3}, nil, 4},
		{"CALL P,nn (not taken)", []byte{0xF4, 0x00, 0x90}, setFlagsNotTakenFor(6), 10},
		{"PUSH AF", []byte{0xF5}, nil, 11},
		{"OR n", []byte{0xF6, 0x42}, nil, 7},
		{"RST 30", []byte{0xF7}, nil, 11},
		{"RET M (not taken)", []byte{0xF8}, setFlagsNotTakenFor(7), 5},
		{"LD SP,HL", []byte{0xF9}, nil, 6},
		{"JP M,nn (not taken)", []byte{0xFA, 0x00, 0x90}, setFlagsNotTakenFor(7), 10},
		{"EI", []byte{0xFB}, nil, 4},
		{"CALL M,nn (not taken)", []byte{0xFC, 0x00, 0x90}, setFlagsNotTakenFor(7), 10},
		// 0xFD prefix — skipped
		{"CP n", []byte{0xFE, 0x42}, nil, 7},
		{"RST 38", []byte{0xFF}, nil, 11},
	}
	cases = append(cases, moreCases...)

	for _, c := range cases {
		got := runOpcodeWithSetup(t, c.bytes, c.setup)
		if got != c.want {
			t.Errorf("%s: t = %d, want %d", c.name, got, c.want)
		}
	}
}

// regName returns the canonical register name for the 3-bit register
// encoding used in Z80 instruction tables: 0=B 1=C 2=D 3=E 4=H 5=L
// 6=(HL) 7=A.
func regName(idx int) string {
	switch idx {
	case 0:
		return "B"
	case 1:
		return "C"
	case 2:
		return "D"
	case 3:
		return "E"
	case 4:
		return "H"
	case 5:
		return "L"
	case 6:
		return "(HL)"
	case 7:
		return "A"
	}
	return "?"
}

// aluName returns the ALU op for bits 5:3 of an 0x80..0xBF base
// opcode: 0=ADD 1=ADC 2=SUB 3=SBC 4=AND 5=XOR 6=OR 7=CP.
func aluName(idx int) string {
	switch idx {
	case 0:
		return "ADD"
	case 1:
		return "ADC"
	case 2:
		return "SUB"
	case 3:
		return "SBC"
	case 4:
		return "AND"
	case 5:
		return "XOR"
	case 6:
		return "OR"
	case 7:
		return "CP"
	}
	return "?"
}

// TestTimingTable_CBOpcodes — full 256-entry CB-prefix T-state table.
// Per Sean Young:
//
//	$00..$3F  RLC/RRC/RL/RR/SLA/SRA/SLL/SRL r or (HL)
//	          register: 8 t, (HL): 15 t
//	$40..$7F  BIT n,r or BIT n,(HL)
//	          register: 8 t, (HL): 12 t
//	$80..$BF  RES n,r or RES n,(HL)
//	          register: 8 t, (HL): 15 t
//	$C0..$FF  SET n,r or SET n,(HL)
//	          register: 8 t, (HL): 15 t
//
// The (HL) form is encoded by (op & 7) == 6.
func TestTimingTable_CBOpcodes(t *testing.T) {
	hlSetup := func(c *CPU, _ memoryWriter) { c.H, c.L = 0x90, 0x00 }
	for op := 0; op <= 0xFF; op++ {
		touchesHL := (op & 7) == 6
		isBIT := op >= 0x40 && op <= 0x7F
		var want uint64
		var setup func(*CPU, memoryWriter)
		if touchesHL {
			setup = hlSetup
			if isBIT {
				want = 12
			} else {
				want = 15
			}
		} else {
			want = 8
		}
		got := runOpcodeWithSetup(t, []byte{0xCB, byte(op)}, setup)
		if got != want {
			t.Errorf("CB $%02X: t = %d, want %d (touchesHL=%v isBIT=%v)",
				op, got, want, touchesHL, isBIT)
		}
	}
}

// TestTimingTable_DDOpcodes — DD-prefix (IX) T-state coverage.
// Covers every documented DD-prefixed instruction shape plus the
// undocumented IXh/IXl 8-bit accesses.
//
// Per Sean Young §A.1: DD adds 4 t to the base instruction's prefix
// fetch. Instructions that fold IX into HL substitute IX+d via an
// extra displacement byte, raising the cost to 19 t (read-modify
// patterns become 23 t).
func TestTimingTable_DDOpcodes(t *testing.T) {
	// Helper: assert (bytes, expected, name).
	check := func(name string, bytes []byte, want uint64) {
		got := runOpcode(t, bytes)
		if got != want {
			t.Errorf("%s [% X]: t = %d, want %d", name, bytes, got, want)
		}
	}
	// 16-bit IX ops.
	check("ADD IX,BC", []byte{0xDD, 0x09}, 15)
	check("ADD IX,DE", []byte{0xDD, 0x19}, 15)
	check("ADD IX,IX", []byte{0xDD, 0x29}, 15)
	check("ADD IX,SP", []byte{0xDD, 0x39}, 15)
	check("LD IX,nn", []byte{0xDD, 0x21, 0x34, 0x12}, 14)
	check("LD (nn),IX", []byte{0xDD, 0x22, 0x00, 0x90}, 20)
	check("LD IX,(nn)", []byte{0xDD, 0x2A, 0x00, 0x90}, 20)
	check("INC IX", []byte{0xDD, 0x23}, 10)
	check("DEC IX", []byte{0xDD, 0x2B}, 10)
	// IXh/IXl 8-bit (undocumented).
	check("INC IXh", []byte{0xDD, 0x24}, 8)
	check("DEC IXh", []byte{0xDD, 0x25}, 8)
	check("LD IXh,n", []byte{0xDD, 0x26, 0x42}, 11)
	check("INC IXl", []byte{0xDD, 0x2C}, 8)
	check("DEC IXl", []byte{0xDD, 0x2D}, 8)
	check("LD IXl,n", []byte{0xDD, 0x2E, 0x42}, 11)
	// (IX+d) read-modify-write.
	check("INC (IX+d)", []byte{0xDD, 0x34, 0x00}, 23)
	check("DEC (IX+d)", []byte{0xDD, 0x35, 0x00}, 23)
	check("LD (IX+d),n", []byte{0xDD, 0x36, 0x00, 0x42}, 19)
	// LD r,(IX+d) and LD (IX+d),r — all 7 src/dst register variants.
	for _, op := range []byte{0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x7E} {
		check(fmt.Sprintf("LD r,(IX+d) %02X", op), []byte{0xDD, op, 0x00}, 19)
	}
	for _, op := range []byte{0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x77} {
		check(fmt.Sprintf("LD (IX+d),r %02X", op), []byte{0xDD, op, 0x00}, 19)
	}
	// ALU A,(IX+d) — 8 ops × (IX+d) src ($86 ADD, $8E ADC, $96 SUB,
	// $9E SBC, $A6 AND, $AE XOR, $B6 OR, $BE CP).
	for _, op := range []byte{0x86, 0x8E, 0x96, 0x9E, 0xA6, 0xAE, 0xB6, 0xBE} {
		check(fmt.Sprintf("ALU A,(IX+d) %02X", op), []byte{0xDD, op, 0x00}, 19)
	}
	// IXh/IXl 8-bit moves (undocumented). 8 t each.
	// LD r,IXh = $44 + r*8 (r=B/C/D/E/A — not H/L because those become IXh/IXl).
	for _, op := range []byte{0x44, 0x4C, 0x54, 0x5C, 0x7C} { // LD B/C/D/E/A, IXh
		check(fmt.Sprintf("LD r,IXh %02X", op), []byte{0xDD, op}, 8)
	}
	for _, op := range []byte{0x45, 0x4D, 0x55, 0x5D, 0x7D} { // LD B/C/D/E/A, IXl
		check(fmt.Sprintf("LD r,IXl %02X", op), []byte{0xDD, op}, 8)
	}
	// LD IXh,r and LD IXl,r.
	for _, op := range []byte{0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x67} {
		check(fmt.Sprintf("LD IXh,r %02X", op), []byte{0xDD, op}, 8)
	}
	for _, op := range []byte{0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6F} {
		check(fmt.Sprintf("LD IXl,r %02X", op), []byte{0xDD, op}, 8)
	}
	// ALU A,IXh and A,IXl — 8 each.
	for _, op := range []byte{0x84, 0x8C, 0x94, 0x9C, 0xA4, 0xAC, 0xB4, 0xBC} {
		check(fmt.Sprintf("ALU A,IXh %02X", op), []byte{0xDD, op}, 8)
	}
	for _, op := range []byte{0x85, 0x8D, 0x95, 0x9D, 0xA5, 0xAD, 0xB5, 0xBD} {
		check(fmt.Sprintf("ALU A,IXl %02X", op), []byte{0xDD, op}, 8)
	}
	// Stack + misc.
	check("PUSH IX", []byte{0xDD, 0xE5}, 15)
	check("POP IX", []byte{0xDD, 0xE1}, 14)
	check("EX (SP),IX", []byte{0xDD, 0xE3}, 23)
	check("JP (IX)", []byte{0xDD, 0xE9}, 8)
	check("LD SP,IX", []byte{0xDD, 0xF9}, 10)
}

// TestTimingTable_FDOpcodes is the IY-prefix analogue — exact same
// timing shape as DD, just with different prefix byte. Tests pin
// down the symmetry so a regression on DD vs FD dispatch shows up.
func TestTimingTable_FDOpcodes(t *testing.T) {
	check := func(name string, bytes []byte, want uint64) {
		got := runOpcode(t, bytes)
		if got != want {
			t.Errorf("%s [% X]: t = %d, want %d", name, bytes, got, want)
		}
	}
	check("ADD IY,BC", []byte{0xFD, 0x09}, 15)
	check("LD IY,nn", []byte{0xFD, 0x21, 0x34, 0x12}, 14)
	check("LD (nn),IY", []byte{0xFD, 0x22, 0x00, 0x90}, 20)
	check("INC IY", []byte{0xFD, 0x23}, 10)
	check("LD IY,(nn)", []byte{0xFD, 0x2A, 0x00, 0x90}, 20)
	check("DEC IY", []byte{0xFD, 0x2B}, 10)
	check("INC IYh", []byte{0xFD, 0x24}, 8)
	check("DEC IYh", []byte{0xFD, 0x25}, 8)
	check("INC (IY+d)", []byte{0xFD, 0x34, 0x00}, 23)
	check("LD (IY+d),n", []byte{0xFD, 0x36, 0x00, 0x42}, 19)
	check("LD A,(IY+d)", []byte{0xFD, 0x7E, 0x00}, 19)
	check("LD (IY+d),A", []byte{0xFD, 0x77, 0x00}, 19)
	check("ADD A,(IY+d)", []byte{0xFD, 0x86, 0x00}, 19)
	check("PUSH IY", []byte{0xFD, 0xE5}, 15)
	check("POP IY", []byte{0xFD, 0xE1}, 14)
	check("EX (SP),IY", []byte{0xFD, 0xE3}, 23)
	check("JP (IY)", []byte{0xFD, 0xE9}, 8)
	check("LD SP,IY", []byte{0xFD, 0xF9}, 10)
}

// TestTimingTable_DDCBOpcodes — DD CB d xx instruction timings. The
// d byte is the index displacement; xx is the CB-style sub-opcode.
//
//	BIT n,(IX+d): 20 t
//	RES/SET/RLC/RRC/RL/RR/SLA/SRA/SLL/SRL n,(IX+d): 23 t
//
// Every undocumented "store to register" form (xx = $00..$3F not
// ending in 6, $80..$FF not ending in 6) ALSO has 23 t cost — the
// FPGA + real Z80 both perform the read-modify-write at (IX+d) and
// then copy the result into the named register.
func TestTimingTable_DDCBOpcodes(t *testing.T) {
	for xx := 0; xx <= 0xFF; xx++ {
		bytes := []byte{0xDD, 0xCB, 0x00, byte(xx)}
		isBIT := xx >= 0x40 && xx <= 0x7F
		var want uint64
		if isBIT {
			want = 20
		} else {
			want = 23
		}
		got := runOpcode(t, bytes)
		if got != want {
			t.Errorf("DD CB 00 %02X: t = %d, want %d", xx, got, want)
		}
	}
}

// TestTimingTable_FDCBOpcodes — symmetric FD CB d xx table.
func TestTimingTable_FDCBOpcodes(t *testing.T) {
	for xx := 0; xx <= 0xFF; xx++ {
		bytes := []byte{0xFD, 0xCB, 0x00, byte(xx)}
		isBIT := xx >= 0x40 && xx <= 0x7F
		var want uint64
		if isBIT {
			want = 20
		} else {
			want = 23
		}
		got := runOpcode(t, bytes)
		if got != want {
			t.Errorf("FD CB 00 %02X: t = %d, want %d", xx, got, want)
		}
	}
}

// TestTimingTable_EDOpcodes — ED-prefix coverage.
//
// Per Sean Young §A.1: ED is a real M1 + opcode, so most ED opcodes
// cost base + 4 t. The exceptions fall in three groups:
//
//	IN r,(C) / OUT (C),r       : 12 t
//	LD I/R,A / LD A,I/R / IM x : 9 t
//	LD rr,(nn) / LD (nn),rr    : 20 t
//	ADC HL,rr / SBC HL,rr      : 15 t
//	NEG / RETI / RETN          : 8 / 14 / 14 t
//	LDI/LDD/CPI/CPD/INI/IND/OUTI/OUTD : 16 t
//	LDIR/LDDR/CPIR/CPDR/INIR/INDR/OTIR/OTDR taken: 21 t,
//	                                   not-taken: 16 t
//
// We don't enumerate every undocumented mirror — those are covered
// by their explicit per-mirror tests in z80_test.go.
func TestTimingTable_EDOpcodes(t *testing.T) {
	check := func(name string, bytes []byte, want uint64) {
		got := runOpcode(t, bytes)
		if got != want {
			t.Errorf("%s [% X]: t = %d, want %d", name, bytes, got, want)
		}
	}
	// IN r,(C) / OUT (C),r — 8 of each = 16 opcodes, all 12 t.
	for _, op := range []byte{0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x70, 0x78} {
		check(fmt.Sprintf("IN r,(C) %02X", op), []byte{0xED, op}, 12)
	}
	for _, op := range []byte{0x41, 0x49, 0x51, 0x59, 0x61, 0x69, 0x71, 0x79} {
		check(fmt.Sprintf("OUT (C),r %02X", op), []byte{0xED, op}, 12)
	}
	// LD I,A / LD R,A / LD A,I / LD A,R — 9 t.
	check("LD I,A", []byte{0xED, 0x47}, 9)
	check("LD R,A", []byte{0xED, 0x4F}, 9)
	check("LD A,I", []byte{0xED, 0x57}, 9)
	check("LD A,R", []byte{0xED, 0x5F}, 9)
	// IM 0/0*/1/2 — 8 t each.
	for _, op := range []byte{0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x76, 0x7E} {
		check(fmt.Sprintf("IM x %02X", op), []byte{0xED, op}, 8)
	}
	// LD (nn),rr / LD rr,(nn) — 20 t each.
	for _, op := range []byte{0x43, 0x53, 0x63, 0x73} {
		check(fmt.Sprintf("LD (nn),rr %02X", op), []byte{0xED, op, 0x00, 0x90}, 20)
	}
	for _, op := range []byte{0x4B, 0x5B, 0x6B, 0x7B} {
		check(fmt.Sprintf("LD rr,(nn) %02X", op), []byte{0xED, op, 0x00, 0x90}, 20)
	}
	// ADC HL,rr / SBC HL,rr — 15 t each.
	for _, op := range []byte{0x42, 0x52, 0x62, 0x72} {
		check(fmt.Sprintf("SBC HL,rr %02X", op), []byte{0xED, op}, 15)
	}
	for _, op := range []byte{0x4A, 0x5A, 0x6A, 0x7A} {
		check(fmt.Sprintf("ADC HL,rr %02X", op), []byte{0xED, op}, 15)
	}
	// NEG (all 8 mirrors) — 8 t.
	for _, op := range []byte{0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C} {
		check(fmt.Sprintf("NEG %02X", op), []byte{0xED, op}, 8)
	}
	// RETI / RETN mirrors — 14 t.
	for _, op := range []byte{0x4D, 0x5D, 0x6D, 0x7D} {
		check(fmt.Sprintf("RETI %02X", op), []byte{0xED, op}, 14)
	}
	for _, op := range []byte{0x45, 0x55, 0x65, 0x75} {
		check(fmt.Sprintf("RETN %02X", op), []byte{0xED, op}, 14)
	}
	// RRD/RLD — 18 t.
	check("RRD", []byte{0xED, 0x67}, 18)
	check("RLD", []byte{0xED, 0x6F}, 18)
	// Block I/O single-step (LDI/LDD/CPI/CPD/INI/IND/OUTI/OUTD) — 16 t.
	for _, op := range []byte{0xA0, 0xA8, 0xA1, 0xA9, 0xA2, 0xAA, 0xA3, 0xAB} {
		check(fmt.Sprintf("BLOCK %02X", op), []byte{0xED, op}, 16)
	}
	// Block-repeat NOT taken — counter decrements to 0.
	// Data ops (LDIR/LDDR/CPIR/CPDR) loop on BC (16-bit); I/O ops
	// (INIR/INDR/OTIR/OTDR) loop on B (8-bit, single register).
	notTakenBC := func(c *CPU, _ memoryWriter) { c.B, c.C = 0, 1 }
	notTakenB := func(c *CPU, _ memoryWriter) { c.B = 1 }
	for _, op := range []byte{0xB0, 0xB8, 0xB1, 0xB9} { // LDIR/LDDR/CPIR/CPDR
		got := runOpcodeWithSetup(t, []byte{0xED, op}, notTakenBC)
		if got != 16 {
			t.Errorf("LDIR/CPIR-family %02X (BC=1 → not taken): t = %d, want 16", op, got)
		}
	}
	for _, op := range []byte{0xB2, 0xBA, 0xB3, 0xBB} { // INIR/INDR/OTIR/OTDR
		got := runOpcodeWithSetup(t, []byte{0xED, op}, notTakenB)
		if got != 16 {
			t.Errorf("INIR/OTIR-family %02X (B=1 → not taken): t = %d, want 16", op, got)
		}
	}
	// Block-repeat TAKEN — one repeat.
	takenBC := func(c *CPU, _ memoryWriter) { c.B, c.C = 0, 2 }
	takenB := func(c *CPU, _ memoryWriter) { c.B = 2 }
	for _, op := range []byte{0xB0, 0xB8, 0xB1, 0xB9} {
		got := runOpcodeWithSetup(t, []byte{0xED, op}, takenBC)
		if got != 21 {
			t.Errorf("LDIR/CPIR-family %02X (BC=2 → taken): t = %d, want 21", op, got)
		}
	}
	for _, op := range []byte{0xB2, 0xBA, 0xB3, 0xBB} {
		got := runOpcodeWithSetup(t, []byte{0xED, op}, takenB)
		if got != 21 {
			t.Errorf("INIR/OTIR-family %02X (B=2 → taken): t = %d, want 21", op, got)
		}
	}
}

// TestTimingTable_BaseConditionalTaken pins the TAKEN path costs for
// all conditional control-flow opcodes. The not-taken paths live in
// TestTimingTable_BaseOpcodes above.
func TestTimingTable_BaseConditionalTaken(t *testing.T) {
	// Flag-true setups: opposite of the "not taken" pattern.
	flagsTakenFor := func(cc int) func(*CPU, memoryWriter) {
		return func(c *CPU, _ memoryWriter) {
			switch cc {
			case 0: // NZ taken → Z=0
				c.F &^= FLAG_Z
			case 1: // Z taken → Z=1
				c.F |= FLAG_Z
			case 2: // NC taken → C=0
				c.F &^= FLAG_C
			case 3: // C taken → C=1
				c.F |= FLAG_C
			case 4: // PO taken → PV=0
				c.F &^= FLAG_PV
			case 5: // PE taken → PV=1
				c.F |= FLAG_PV
			case 6: // P taken → S=0
				c.F &^= FLAG_S
			case 7: // M taken → S=1
				c.F |= FLAG_S
			}
		}
	}
	// JR cc taken = 12 t; opcodes $20/28/30/38.
	for i, op := range []byte{0x20, 0x28, 0x30, 0x38} {
		cc := i // NZ, Z, NC, C
		got := runOpcodeWithSetup(t, []byte{op, 0x05}, flagsTakenFor(cc))
		if got != 12 {
			t.Errorf("JR cc=%d $%02X taken: t = %d, want 12", cc, op, got)
		}
	}
	// JP cc taken = 10 t (same as not taken — JP cost is flag-independent).
	for i, op := range []byte{0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA} {
		cc := i
		got := runOpcodeWithSetup(t, []byte{op, 0x00, 0x90}, flagsTakenFor(cc))
		if got != 10 {
			t.Errorf("JP cc=%d $%02X taken: t = %d, want 10", cc, op, got)
		}
	}
	// CALL cc taken = 17 t.
	for i, op := range []byte{0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC} {
		cc := i
		got := runOpcodeWithSetup(t, []byte{op, 0x00, 0x90}, flagsTakenFor(cc))
		if got != 17 {
			t.Errorf("CALL cc=%d $%02X taken: t = %d, want 17", cc, op, got)
		}
	}
	// RET cc taken = 11 t.
	for i, op := range []byte{0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0, 0xF8} {
		cc := i
		got := runOpcodeWithSetup(t, []byte{op}, flagsTakenFor(cc))
		if got != 11 {
			t.Errorf("RET cc=%d $%02X taken: t = %d, want 11", cc, op, got)
		}
	}
	// DJNZ taken = 13 t (B was 2 → 1, branch taken).
	got := runOpcodeWithSetup(t, []byte{0x10, 0x05}, func(c *CPU, _ memoryWriter) { c.B = 2 })
	if got != 13 {
		t.Errorf("DJNZ taken: t = %d, want 13", got)
	}
}
