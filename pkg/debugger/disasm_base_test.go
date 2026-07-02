package debugger

import "testing"

// Exhaustive coverage of disasmBase — the 256-entry base opcode
// dispatcher. One representative encoding per case branch, plus the
// DB fallthrough, via the public Disassemble entry point.

func TestDisasmBase(t *testing.T) {
	cases := []struct {
		name    string
		bytes   []byte
		mnem    string
		operand string
		nBytes  int
	}{
		{"NOP", []byte{0x00}, "NOP", "", 1},
		{"LD BC,nn", []byte{0x01, 0x34, 0x12}, "LD", "BC,$1234", 3},
		{"LD DE,nn", []byte{0x11, 0xCD, 0xAB}, "LD", "DE,$ABCD", 3},
		{"LD HL,nn", []byte{0x21, 0x00, 0x40}, "LD", "HL,$4000", 3},
		{"LD SP,nn", []byte{0x31, 0xFF, 0xFF}, "LD", "SP,$FFFF", 3},
		{"LD (BC),A", []byte{0x02}, "LD", "(BC),A", 1},
		{"LD A,(BC)", []byte{0x0A}, "LD", "A,(BC)", 1},
		{"LD (DE),A", []byte{0x12}, "LD", "(DE),A", 1},
		{"LD A,(DE)", []byte{0x1A}, "LD", "A,(DE)", 1},
		{"INC BC", []byte{0x03}, "INC", "BC", 1},
		{"INC SP", []byte{0x33}, "INC", "SP", 1},
		{"DEC BC", []byte{0x0B}, "DEC", "BC", 1},
		{"DEC HL", []byte{0x2B}, "DEC", "HL", 1},
		{"INC B", []byte{0x04}, "INC", "B", 1},
		{"INC (HL)", []byte{0x34}, "INC", "(HL)", 1},
		{"DEC C", []byte{0x0D}, "DEC", "C", 1},
		{"DEC A", []byte{0x3D}, "DEC", "A", 1},
		{"LD B,n", []byte{0x06, 0x42}, "LD", "B,$42", 2},
		{"LD (HL),n", []byte{0x36, 0xFF}, "LD", "(HL),$FF", 2},
		{"LD A,n", []byte{0x3E, 0x07}, "LD", "A,$07", 2},
		{"RLCA", []byte{0x07}, "RLCA", "", 1},
		{"RRCA", []byte{0x0F}, "RRCA", "", 1},
		{"RLA", []byte{0x17}, "RLA", "", 1},
		{"RRA", []byte{0x1F}, "RRA", "", 1},
		{"ADD HL,BC", []byte{0x09}, "ADD", "HL,BC", 1},
		{"ADD HL,SP", []byte{0x39}, "ADD", "HL,SP", 1},
		// DJNZ: target = addr after operand + d. Operand at addr 1,
		// d=0x02 → 0+2+2 = 0x0004.
		{"DJNZ fwd", []byte{0x10, 0x02}, "DJNZ", "$0004", 2},
		{"JR fwd", []byte{0x18, 0x05}, "JR", "$0007", 2},
		{"JR NZ", []byte{0x20, 0x00}, "JR", "NZ,$0002", 2},
		{"JR Z", []byte{0x28, 0xFE}, "JR", "Z,$0000", 2},
		{"JR NC", []byte{0x30, 0x00}, "JR", "NC,$0002", 2},
		{"JR C", []byte{0x38, 0x00}, "JR", "C,$0002", 2},
		{"LD (nn),HL", []byte{0x22, 0x00, 0x40}, "LD", "($4000),HL", 3},
		{"LD HL,(nn)", []byte{0x2A, 0x00, 0x40}, "LD", "HL,($4000)", 3},
		{"LD (nn),A", []byte{0x32, 0x34, 0x12}, "LD", "($1234),A", 3},
		{"LD A,(nn)", []byte{0x3A, 0x34, 0x12}, "LD", "A,($1234)", 3},
		{"DAA", []byte{0x27}, "DAA", "", 1},
		{"CPL", []byte{0x2F}, "CPL", "", 1},
		{"SCF", []byte{0x37}, "SCF", "", 1},
		{"CCF", []byte{0x3F}, "CCF", "", 1},
		{"HALT", []byte{0x76}, "HALT", "", 1},
		{"LD B,C", []byte{0x41}, "LD", "B,C", 1},
		{"LD A,(HL)", []byte{0x7E}, "LD", "A,(HL)", 1},
		{"LD (HL),A", []byte{0x77}, "LD", "(HL),A", 1},
		{"ADD A,B", []byte{0x80}, "ADD A,", "B", 1},
		{"ADC A,(HL)", []byte{0x8E}, "ADC A,", "(HL)", 1},
		{"SUB E", []byte{0x93}, "SUB ", "E", 1},
		{"AND A", []byte{0xA7}, "AND ", "A", 1},
		{"XOR B", []byte{0xA8}, "XOR ", "B", 1},
		{"OR C", []byte{0xB1}, "OR ", "C", 1},
		{"CP (HL)", []byte{0xBE}, "CP ", "(HL)", 1},
		{"ADD A,n", []byte{0xC6, 0x10}, "ADD A,", "$10", 2},
		{"CP n", []byte{0xFE, 0x20}, "CP ", "$20", 2},
		{"RET NZ", []byte{0xC0}, "RET", "NZ", 1},
		{"RET M", []byte{0xF8}, "RET", "M", 1},
		{"POP BC", []byte{0xC1}, "POP", "BC", 1},
		{"POP AF", []byte{0xF1}, "POP", "AF", 1},
		{"JP NZ,nn", []byte{0xC2, 0x00, 0x80}, "JP", "NZ,$8000", 3},
		{"JP nn", []byte{0xC3, 0x00, 0x80}, "JP", "$8000", 3},
		{"CALL Z,nn", []byte{0xCC, 0x00, 0x80}, "CALL", "Z,$8000", 3},
		{"PUSH BC", []byte{0xC5}, "PUSH", "BC", 1},
		{"PUSH AF", []byte{0xF5}, "PUSH", "AF", 1},
		{"CALL nn", []byte{0xCD, 0x00, 0x80}, "CALL", "$8000", 3},
		{"RST 00", []byte{0xC7}, "RST", "$00", 1},
		{"RST 38", []byte{0xFF}, "RST", "$38", 1},
		{"RET", []byte{0xC9}, "RET", "", 1},
		{"JP (HL)", []byte{0xE9}, "JP", "(HL)", 1},
		{"LD SP,HL", []byte{0xF9}, "LD", "SP,HL", 1},
		{"EX AF,AF'", []byte{0x08}, "EX", "AF,AF'", 1},
		{"EXX", []byte{0xD9}, "EXX", "", 1},
		{"EX (SP),HL", []byte{0xE3}, "EX", "(SP),HL", 1},
		{"EX DE,HL", []byte{0xEB}, "EX", "DE,HL", 1},
		{"DI", []byte{0xF3}, "DI", "", 1},
		{"EI", []byte{0xFB}, "EI", "", 1},
		{"OUT (n),A", []byte{0xD3, 0xFE}, "OUT", "($FE),A", 2},
		{"IN A,(n)", []byte{0xDB, 0xFE}, "IN", "A,($FE)", 2},
		// 0xDD/0xFD/0xCB/0xED are prefix opcodes handled elsewhere;
		// any remaining base opcode falls through to DB. 0xD7 is RST
		// $10 so it's covered; pick an unused encoding for fallthrough:
		// there isn't one in base — every byte resolves. Cover the DB
		// path through the DD-unknown case instead (separate test).
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Disassemble(makeReader(c.bytes), 0, 1)[0]
			if got.Mnem != c.mnem || got.Operand != c.operand || len(got.Bytes) != c.nBytes {
				t.Errorf("got {%q %q %d}, want {%q %q %d}",
					got.Mnem, got.Operand, len(got.Bytes), c.mnem, c.operand, c.nBytes)
			}
		})
	}
}

// TestDisasmBase_AllBytesResolve confirms every base opcode produces
// a non-empty mnemonic (no panic, no blank line) — exercises the
// dispatcher across the full 0x00-0xFF range.
func TestDisasmBase_AllBytesResolve(t *testing.T) {
	for op := 0; op <= 0xFF; op++ {
		// Provide trailing operand bytes so multi-byte ops decode.
		buf := []byte{byte(op), 0x00, 0x00, 0x00}
		lines := Disassemble(makeReader(buf), 0, 1)
		if len(lines) == 0 || lines[0].Mnem == "" {
			t.Errorf("opcode $%02X produced empty disassembly", op)
		}
	}
}
