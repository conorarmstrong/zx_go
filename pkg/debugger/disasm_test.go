package debugger

import "testing"

// makeReader returns a readFunc that serves the given bytes starting
// at address 0; out-of-range reads return 0xFF.
func makeReader(b []byte) readFunc {
	return func(addr uint16) byte {
		if int(addr) >= len(b) {
			return 0xFF
		}
		return b[addr]
	}
}

func TestDisassembleZ80N(t *testing.T) {
	cases := []struct {
		name    string
		bytes   []byte
		mnem    string
		operand string
		nBytes  int
	}{
		{"SWAPNIB", []byte{0xED, 0x23}, "SWAPNIB", "", 2},
		{"MIRROR", []byte{0xED, 0x24}, "MIRROR", "A", 2},
		{"TEST", []byte{0xED, 0x27, 0x55}, "TEST", "$55", 3},
		{"BSLA", []byte{0xED, 0x28}, "BSLA", "DE,B", 2},
		{"BSRA", []byte{0xED, 0x29}, "BSRA", "DE,B", 2},
		{"BSRL", []byte{0xED, 0x2A}, "BSRL", "DE,B", 2},
		{"BSRF", []byte{0xED, 0x2B}, "BSRF", "DE,B", 2},
		{"BRLC", []byte{0xED, 0x2C}, "BRLC", "DE,B", 2},
		{"MUL", []byte{0xED, 0x30}, "MUL", "D,E", 2},
		{"ADD HL,A", []byte{0xED, 0x31}, "ADD", "HL,A", 2},
		{"ADD DE,A", []byte{0xED, 0x32}, "ADD", "DE,A", 2},
		{"ADD BC,A", []byte{0xED, 0x33}, "ADD", "BC,A", 2},
		{"ADD HL,nn", []byte{0xED, 0x34, 0x34, 0x12}, "ADD", "HL,$1234", 4},
		{"ADD DE,nn", []byte{0xED, 0x35, 0xCD, 0xAB}, "ADD", "DE,$ABCD", 4},
		{"ADD BC,nn", []byte{0xED, 0x36, 0xFF, 0xFF}, "ADD", "BC,$FFFF", 4},
		// PUSH nn: operand is big-endian per Z80N spec.
		{"PUSH nn", []byte{0xED, 0x8A, 0x12, 0x34}, "PUSH", "$1234", 4},
		{"OUTINB", []byte{0xED, 0x90}, "OUTINB", "", 2},
		{"NEXTREG r,n", []byte{0xED, 0x91, 0x8E, 0x03}, "NEXTREG", "$8E,$03", 4},
		{"NEXTREG r,A", []byte{0xED, 0x92, 0x07}, "NEXTREG", "$07,A", 3},
		{"PIXELDN", []byte{0xED, 0x93}, "PIXELDN", "", 2},
		{"PIXELAD", []byte{0xED, 0x94}, "PIXELAD", "", 2},
		{"SETAE", []byte{0xED, 0x95}, "SETAE", "", 2},
		{"JP (C)", []byte{0xED, 0x98}, "JP", "(C)", 2},
		{"LDIX", []byte{0xED, 0xA4}, "LDIX", "", 2},
		{"LDWS", []byte{0xED, 0xA5}, "LDWS", "", 2},
		{"LDDX", []byte{0xED, 0xAC}, "LDDX", "", 2},
		{"LDIRX", []byte{0xED, 0xB4}, "LDIRX", "", 2},
		{"LDPIRX", []byte{0xED, 0xB7}, "LDPIRX", "", 2},
		{"LDDRX", []byte{0xED, 0xBC}, "LDDRX", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := Disassemble(makeReader(c.bytes), 0, 1)
			if len(lines) == 0 {
				t.Fatalf("no disassembly produced")
			}
			got := lines[0]
			if got.Mnem != c.mnem {
				t.Errorf("mnemonic = %q, want %q", got.Mnem, c.mnem)
			}
			if got.Operand != c.operand {
				t.Errorf("operand = %q, want %q", got.Operand, c.operand)
			}
			if len(got.Bytes) != c.nBytes {
				t.Errorf("length = %d, want %d", len(got.Bytes), c.nBytes)
			}
		})
	}
}
