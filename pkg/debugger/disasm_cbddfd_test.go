package debugger

import "testing"

// Covers disasmCB (CB-prefixed rotates/BIT/RES/SET) and disasmDDFD
// (IX/IY-prefixed loads/ALU/DDCB) via the public Disassemble entry
// point.

func TestDisasmCB(t *testing.T) {
	cases := []struct {
		name    string
		bytes   []byte
		mnem    string
		operand string
		nBytes  int
	}{
		// Rotates/shifts (op < 0x40): rot[op>>3], r8[op&7].
		{"RLC B", []byte{0xCB, 0x00}, "RLC", "B", 2},
		{"RRC C", []byte{0xCB, 0x09}, "RRC", "C", 2},
		{"RL D", []byte{0xCB, 0x12}, "RL", "D", 2},
		{"RR E", []byte{0xCB, 0x1B}, "RR", "E", 2},
		{"SLA H", []byte{0xCB, 0x24}, "SLA", "H", 2},
		{"SRA L", []byte{0xCB, 0x2D}, "SRA", "L", 2},
		{"SLL (HL)", []byte{0xCB, 0x36}, "SLL", "(HL)", 2},
		{"SRL A", []byte{0xCB, 0x3F}, "SRL", "A", 2},
		// BIT (0x40-0x7F).
		{"BIT 0,B", []byte{0xCB, 0x40}, "BIT", "0,B", 2},
		{"BIT 7,A", []byte{0xCB, 0x7F}, "BIT", "7,A", 2},
		{"BIT 3,(HL)", []byte{0xCB, 0x5E}, "BIT", "3,(HL)", 2},
		// RES (0x80-0xBF).
		{"RES 0,B", []byte{0xCB, 0x80}, "RES", "0,B", 2},
		{"RES 7,A", []byte{0xCB, 0xBF}, "RES", "7,A", 2},
		// SET (0xC0-0xFF).
		{"SET 0,B", []byte{0xCB, 0xC0}, "SET", "0,B", 2},
		{"SET 7,A", []byte{0xCB, 0xFF}, "SET", "7,A", 2},
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

func TestDisasmDDFD_IX(t *testing.T) {
	cases := []struct {
		name    string
		bytes   []byte
		mnem    string
		operand string
		nBytes  int
	}{
		{"ADD IX,BC", []byte{0xDD, 0x09}, "ADD", "IX,BC", 2},
		{"ADD IX,IX", []byte{0xDD, 0x29}, "ADD", "IX,IX", 2},
		{"LD IX,nn", []byte{0xDD, 0x21, 0x34, 0x12}, "LD", "IX,$1234", 4},
		{"LD (nn),IX", []byte{0xDD, 0x22, 0x00, 0x40}, "LD", "($4000),IX", 4},
		{"LD IX,(nn)", []byte{0xDD, 0x2A, 0x00, 0x40}, "LD", "IX,($4000)", 4},
		{"INC IX", []byte{0xDD, 0x23}, "INC", "IX", 2},
		{"DEC IX", []byte{0xDD, 0x2B}, "DEC", "IX", 2},
		{"INC (IX+5)", []byte{0xDD, 0x34, 0x05}, "INC", "(IX+5)", 3},
		{"DEC (IX-1)", []byte{0xDD, 0x35, 0xFF}, "DEC", "(IX-1)", 3},
		{"LD (IX+2),n", []byte{0xDD, 0x36, 0x02, 0xAB}, "LD", "(IX+2),$AB", 4},
		{"LD B,(IX+1)", []byte{0xDD, 0x46, 0x01}, "LD", "B,(IX+1)", 3},
		{"LD (IX+3),A", []byte{0xDD, 0x77, 0x03}, "LD", "(IX+3),A", 3},
		{"ADD A,(IX+0)", []byte{0xDD, 0x86, 0x00}, "ADD A,", "(IX+0)", 3},
		{"CP (IX+4)", []byte{0xDD, 0xBE, 0x04}, "CP ", "(IX+4)", 3},
		{"POP IX", []byte{0xDD, 0xE1}, "POP", "IX", 2},
		{"PUSH IX", []byte{0xDD, 0xE5}, "PUSH", "IX", 2},
		{"JP (IX)", []byte{0xDD, 0xE9}, "JP", "(IX)", 2},
		{"LD SP,IX", []byte{0xDD, 0xF9}, "LD", "SP,IX", 2},
		{"EX (SP),IX", []byte{0xDD, 0xE3}, "EX", "(SP),IX", 2},
		// DDCB shift/bit on (IX+d).
		{"RLC (IX+1)", []byte{0xDD, 0xCB, 0x01, 0x06}, "RLC", "(IX+1)", 4},
		{"BIT 2,(IX+1)", []byte{0xDD, 0xCB, 0x01, 0x56}, "BIT", "2,(IX+1)", 4},
		{"RES 0,(IX+1)", []byte{0xDD, 0xCB, 0x01, 0x86}, "RES", "0,(IX+1)", 4},
		{"SET 7,(IX+1)", []byte{0xDD, 0xCB, 0x01, 0xFE}, "SET", "7,(IX+1)", 4},
		// Unknown DD-prefixed op falls through to DB.
		{"DB DD,00", []byte{0xDD, 0x00}, "DB $DD,$00", "", 2},
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

// FD prefix routes the same disasmDDFD with reg="IY".
func TestDisasmDDFD_IY(t *testing.T) {
	got := Disassemble(makeReader([]byte{0xFD, 0x21, 0xCD, 0xAB}), 0, 1)[0]
	if got.Mnem != "LD" || got.Operand != "IY,$ABCD" {
		t.Errorf("FD LD IY,nn = {%q %q}, want {LD IY,$ABCD}", got.Mnem, got.Operand)
	}
	got = Disassemble(makeReader([]byte{0xFD, 0xCB, 0x02, 0x4E}), 0, 1)[0]
	if got.Mnem != "BIT" || got.Operand != "1,(IY+2)" {
		t.Errorf("FDCB BIT = {%q %q}, want {BIT 1,(IY+2)}", got.Mnem, got.Operand)
	}
}
