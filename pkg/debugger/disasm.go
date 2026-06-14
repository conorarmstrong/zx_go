package debugger

import "fmt"

// DisassembledLine represents one disassembled instruction.
type DisassembledLine struct {
	Addr    uint16
	Bytes   []byte
	Mnem    string
	Operand string
}

func (d DisassembledLine) String() string {
	hex := ""
	for _, b := range d.Bytes {
		hex += fmt.Sprintf("%02X ", b)
	}
	for len(hex) < 12 {
		hex += "   "
	}
	if d.Operand != "" {
		return fmt.Sprintf("%04X  %s %s %s", d.Addr, hex, d.Mnem, d.Operand)
	}
	return fmt.Sprintf("%04X  %s %s", d.Addr, hex, d.Mnem)
}

// readFunc is a function that reads a byte from an address.
type readFunc func(addr uint16) byte

// Disassemble disassembles count instructions starting from addr.
func Disassemble(read readFunc, addr uint16, count int) []DisassembledLine {
	var lines []DisassembledLine
	for i := 0; i < count; i++ {
		line := disassembleOne(read, addr)
		lines = append(lines, line)
		addr += uint16(len(line.Bytes))
	}
	return lines
}

func disassembleOne(read readFunc, addr uint16) DisassembledLine {
	start := addr
	op := read(addr)
	addr++

	switch op {
	case 0xCB:
		return disasmCB(read, start, addr)
	case 0xED:
		return disasmED(read, start, addr)
	case 0xDD:
		return disasmDDFD(read, start, addr, "IX")
	case 0xFD:
		return disasmDDFD(read, start, addr, "IY")
	}

	return disasmBase(read, start, addr, op)
}

func bytes(read readFunc, start uint16, count int) []byte {
	b := make([]byte, count)
	for i := 0; i < count; i++ {
		b[i] = read(start + uint16(i))
	}
	return b
}

func disasmBase(read readFunc, start, addr uint16, op byte) DisassembledLine {
	r8 := [8]string{"B", "C", "D", "E", "H", "L", "(HL)", "A"}
	r16 := [4]string{"BC", "DE", "HL", "SP"}
	r16af := [4]string{"BC", "DE", "HL", "AF"}
	cc := [8]string{"NZ", "Z", "NC", "C", "PO", "PE", "P", "M"}
	alu := [8]string{"ADD A,", "ADC A,", "SUB ", "SBC A,", "AND ", "XOR ", "OR ", "CP "}

	mk := func(n int, m, o string) DisassembledLine {
		return DisassembledLine{start, bytes(read, start, n), m, o}
	}

	switch {
	// NOP
	case op == 0x00:
		return mk(1, "NOP", "")
	// LD r16,nn
	case op&0xCF == 0x01:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "LD", fmt.Sprintf("%s,$%04X", r16[(op>>4)&3], nn))
	// LD (BC/DE),A / LD A,(BC/DE)
	case op == 0x02:
		return mk(1, "LD", "(BC),A")
	case op == 0x0A:
		return mk(1, "LD", "A,(BC)")
	case op == 0x12:
		return mk(1, "LD", "(DE),A")
	case op == 0x1A:
		return mk(1, "LD", "A,(DE)")
	// INC/DEC r16
	case op&0xCF == 0x03:
		return mk(1, "INC", r16[(op>>4)&3])
	case op&0xCF == 0x0B:
		return mk(1, "DEC", r16[(op>>4)&3])
	// INC/DEC r8
	case op&0xC7 == 0x04:
		return mk(1, "INC", r8[(op>>3)&7])
	case op&0xC7 == 0x05:
		return mk(1, "DEC", r8[(op>>3)&7])
	// LD r8,n
	case op&0xC7 == 0x06:
		n := read(addr)
		return mk(2, "LD", fmt.Sprintf("%s,$%02X", r8[(op>>3)&7], n))
	// RLCA/RRCA/RLA/RRA
	case op == 0x07:
		return mk(1, "RLCA", "")
	case op == 0x0F:
		return mk(1, "RRCA", "")
	case op == 0x17:
		return mk(1, "RLA", "")
	case op == 0x1F:
		return mk(1, "RRA", "")
	// ADD HL,r16
	case op&0xCF == 0x09:
		return mk(1, "ADD", fmt.Sprintf("HL,%s", r16[(op>>4)&3]))
	// DJNZ
	case op == 0x10:
		d := int8(read(addr))
		target := addr + 1 + uint16(int16(d))
		return mk(2, "DJNZ", fmt.Sprintf("$%04X", target))
	// JR / JR cc
	case op == 0x18:
		d := int8(read(addr))
		target := addr + 1 + uint16(int16(d))
		return mk(2, "JR", fmt.Sprintf("$%04X", target))
	case op == 0x20, op == 0x28, op == 0x30, op == 0x38:
		d := int8(read(addr))
		target := addr + 1 + uint16(int16(d))
		return mk(2, "JR", fmt.Sprintf("%s,$%04X", cc[(op-0x20)>>3], target))
	// LD (nn),HL / LD HL,(nn)
	case op == 0x22:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "LD", fmt.Sprintf("($%04X),HL", nn))
	case op == 0x2A:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "LD", fmt.Sprintf("HL,($%04X)", nn))
	// LD (nn),A / LD A,(nn)
	case op == 0x32:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "LD", fmt.Sprintf("($%04X),A", nn))
	case op == 0x3A:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "LD", fmt.Sprintf("A,($%04X)", nn))
	// DAA/CPL/SCF/CCF
	case op == 0x27:
		return mk(1, "DAA", "")
	case op == 0x2F:
		return mk(1, "CPL", "")
	case op == 0x37:
		return mk(1, "SCF", "")
	case op == 0x3F:
		return mk(1, "CCF", "")
	// LD r,r / HALT
	case op == 0x76:
		return mk(1, "HALT", "")
	case op >= 0x40 && op <= 0x7F:
		return mk(1, "LD", fmt.Sprintf("%s,%s", r8[(op>>3)&7], r8[op&7]))
	// ALU r8
	case op >= 0x80 && op <= 0xBF:
		idx := (op - 0x80) >> 3
		return mk(1, alu[idx], r8[op&7])
	// ALU n
	case op&0xC7 == 0xC6:
		n := read(addr)
		idx := (op - 0xC6) >> 3
		return mk(2, alu[idx], fmt.Sprintf("$%02X", n))
	// RET cc
	case op&0xC7 == 0xC0:
		return mk(1, "RET", cc[(op>>3)&7])
	// POP r16
	case op&0xCF == 0xC1:
		return mk(1, "POP", r16af[(op>>4)&3])
	// JP cc,nn
	case op&0xC7 == 0xC2:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "JP", fmt.Sprintf("%s,$%04X", cc[(op>>3)&7], nn))
	// JP nn
	case op == 0xC3:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "JP", fmt.Sprintf("$%04X", nn))
	// CALL cc,nn
	case op&0xC7 == 0xC4:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "CALL", fmt.Sprintf("%s,$%04X", cc[(op>>3)&7], nn))
	// PUSH r16
	case op&0xCF == 0xC5:
		return mk(1, "PUSH", r16af[(op>>4)&3])
	// CALL nn
	case op == 0xCD:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(3, "CALL", fmt.Sprintf("$%04X", nn))
	// RST
	case op&0xC7 == 0xC7:
		return mk(1, "RST", fmt.Sprintf("$%02X", op&0x38))
	// RET
	case op == 0xC9:
		return mk(1, "RET", "")
	// JP (HL)
	case op == 0xE9:
		return mk(1, "JP", "(HL)")
	// LD SP,HL
	case op == 0xF9:
		return mk(1, "LD", "SP,HL")
	// EX
	case op == 0x08:
		return mk(1, "EX", "AF,AF'")
	case op == 0xD9:
		return mk(1, "EXX", "")
	case op == 0xE3:
		return mk(1, "EX", "(SP),HL")
	case op == 0xEB:
		return mk(1, "EX", "DE,HL")
	// DI/EI
	case op == 0xF3:
		return mk(1, "DI", "")
	case op == 0xFB:
		return mk(1, "EI", "")
	// OUT (n),A / IN A,(n)
	case op == 0xD3:
		n := read(addr)
		return mk(2, "OUT", fmt.Sprintf("($%02X),A", n))
	case op == 0xDB:
		n := read(addr)
		return mk(2, "IN", fmt.Sprintf("A,($%02X)", n))
	}
	return mk(1, fmt.Sprintf("DB $%02X", op), "")
}

func disasmCB(read readFunc, start, addr uint16) DisassembledLine {
	op := read(addr)
	r8 := [8]string{"B", "C", "D", "E", "H", "L", "(HL)", "A"}
	rot := [8]string{"RLC", "RRC", "RL", "RR", "SLA", "SRA", "SLL", "SRL"}
	mk := func(m, o string) DisassembledLine {
		return DisassembledLine{start, bytes(read, start, 2), m, o}
	}
	switch {
	case op < 0x40:
		return mk(rot[op>>3], r8[op&7])
	case op < 0x80:
		return mk("BIT", fmt.Sprintf("%d,%s", (op>>3)&7, r8[op&7]))
	case op < 0xC0:
		return mk("RES", fmt.Sprintf("%d,%s", (op>>3)&7, r8[op&7]))
	default:
		return mk("SET", fmt.Sprintf("%d,%s", (op>>3)&7, r8[op&7]))
	}
}

func disasmED(read readFunc, start, addr uint16) DisassembledLine {
	op := read(addr)
	r16 := [4]string{"BC", "DE", "HL", "SP"}
	mk := func(n int, m, o string) DisassembledLine {
		return DisassembledLine{start, bytes(read, start, n), m, o}
	}
	r8 := [8]string{"B", "C", "D", "E", "H", "L", "F", "A"}

	switch op {
	// IN r,(C)
	case 0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x70, 0x78:
		return mk(2, "IN", fmt.Sprintf("%s,(C)", r8[(op>>3)&7]))
	// OUT (C),r
	case 0x41, 0x49, 0x51, 0x59, 0x61, 0x69, 0x71, 0x79:
		return mk(2, "OUT", fmt.Sprintf("(C),%s", r8[(op>>3)&7]))
	// SBC/ADC HL,r16
	case 0x42, 0x52, 0x62, 0x72:
		return mk(2, "SBC", fmt.Sprintf("HL,%s", r16[(op>>4)&3]))
	case 0x4A, 0x5A, 0x6A, 0x7A:
		return mk(2, "ADC", fmt.Sprintf("HL,%s", r16[(op>>4)&3]))
	// LD (nn),r16 / LD r16,(nn)
	case 0x43, 0x53, 0x63, 0x73:
		nn := uint16(read(addr+1)) | uint16(read(addr+2))<<8
		return mk(4, "LD", fmt.Sprintf("($%04X),%s", nn, r16[(op>>4)&3]))
	case 0x4B, 0x5B, 0x6B, 0x7B:
		nn := uint16(read(addr+1)) | uint16(read(addr+2))<<8
		return mk(4, "LD", fmt.Sprintf("%s,($%04X)", r16[(op>>4)&3], nn))
	// NEG
	case 0x44:
		return mk(2, "NEG", "")
	// RETN/RETI
	case 0x45:
		return mk(2, "RETN", "")
	case 0x4D:
		return mk(2, "RETI", "")
	// IM
	case 0x46:
		return mk(2, "IM", "0")
	case 0x56:
		return mk(2, "IM", "1")
	case 0x5E:
		return mk(2, "IM", "2")
	// LD I,A / LD R,A / LD A,I / LD A,R
	case 0x47:
		return mk(2, "LD", "I,A")
	case 0x4F:
		return mk(2, "LD", "R,A")
	case 0x57:
		return mk(2, "LD", "A,I")
	case 0x5F:
		return mk(2, "LD", "A,R")
	// RRD/RLD
	case 0x67:
		return mk(2, "RRD", "")
	case 0x6F:
		return mk(2, "RLD", "")
	// Block operations
	case 0xA0:
		return mk(2, "LDI", "")
	case 0xA1:
		return mk(2, "CPI", "")
	case 0xA2:
		return mk(2, "INI", "")
	case 0xA3:
		return mk(2, "OUTI", "")
	case 0xA8:
		return mk(2, "LDD", "")
	case 0xA9:
		return mk(2, "CPD", "")
	case 0xAA:
		return mk(2, "IND", "")
	case 0xAB:
		return mk(2, "OUTD", "")
	case 0xB0:
		return mk(2, "LDIR", "")
	case 0xB1:
		return mk(2, "CPIR", "")
	case 0xB2:
		return mk(2, "INIR", "")
	case 0xB3:
		return mk(2, "OTIR", "")
	case 0xB8:
		return mk(2, "LDDR", "")
	case 0xB9:
		return mk(2, "CPDR", "")
	case 0xBA:
		return mk(2, "INDR", "")
	case 0xBB:
		return mk(2, "OTDR", "")

	// --- Z80N extensions (Spectrum Next CPU variant) ---
	case 0x23:
		return mk(2, "SWAPNIB", "")
	case 0x24:
		return mk(2, "MIRROR", "A")
	case 0x27:
		return mk(3, "TEST", fmt.Sprintf("$%02X", read(addr+1)))
	case 0x28:
		return mk(2, "BSLA", "DE,B")
	case 0x29:
		return mk(2, "BSRA", "DE,B")
	case 0x2A:
		return mk(2, "BSRL", "DE,B")
	case 0x2B:
		return mk(2, "BSRF", "DE,B")
	case 0x2C:
		return mk(2, "BRLC", "DE,B")
	case 0x30:
		return mk(2, "MUL", "D,E")
	case 0x31:
		return mk(2, "ADD", "HL,A")
	case 0x32:
		return mk(2, "ADD", "DE,A")
	case 0x33:
		return mk(2, "ADD", "BC,A")
	case 0x34:
		nn := uint16(read(addr+1)) | uint16(read(addr+2))<<8
		return mk(4, "ADD", fmt.Sprintf("HL,$%04X", nn))
	case 0x35:
		nn := uint16(read(addr+1)) | uint16(read(addr+2))<<8
		return mk(4, "ADD", fmt.Sprintf("DE,$%04X", nn))
	case 0x36:
		nn := uint16(read(addr+1)) | uint16(read(addr+2))<<8
		return mk(4, "ADD", fmt.Sprintf("BC,$%04X", nn))
	case 0x8A:
		// PUSH nn — big-endian operand: hi byte first.
		nn := uint16(read(addr+1))<<8 | uint16(read(addr+2))
		return mk(4, "PUSH", fmt.Sprintf("$%04X", nn))
	case 0x90:
		return mk(2, "OUTINB", "")
	case 0x91:
		return mk(4, "NEXTREG",
			fmt.Sprintf("$%02X,$%02X", read(addr+1), read(addr+2)))
	case 0x92:
		return mk(3, "NEXTREG", fmt.Sprintf("$%02X,A", read(addr+1)))
	case 0x93:
		return mk(2, "PIXELDN", "")
	case 0x94:
		return mk(2, "PIXELAD", "")
	case 0x95:
		return mk(2, "SETAE", "")
	case 0x98:
		return mk(2, "JP", "(C)")
	case 0xA4:
		return mk(2, "LDIX", "")
	case 0xA5:
		return mk(2, "LDWS", "")
	case 0xAC:
		return mk(2, "LDDX", "")
	case 0xB4:
		return mk(2, "LDIRX", "")
	case 0xB7:
		return mk(2, "LDPIRX", "")
	case 0xBC:
		return mk(2, "LDDRX", "")
	}
	return mk(2, fmt.Sprintf("DB $ED,$%02X", op), "")
}

func disasmDDFD(read readFunc, start, addr uint16, reg string) DisassembledLine {
	op := read(addr)
	addr++
	mk := func(n int, m, o string) DisassembledLine {
		return DisassembledLine{start, bytes(read, start, n), m, o}
	}

	switch {
	// ADD IX/IY,r16
	case op&0xCF == 0x09:
		r16 := [4]string{"BC", "DE", reg, "SP"}
		return mk(2, "ADD", fmt.Sprintf("%s,%s", reg, r16[(op>>4)&3]))
	// LD IX/IY,nn
	case op == 0x21:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(4, "LD", fmt.Sprintf("%s,$%04X", reg, nn))
	// LD (nn),IX/IY
	case op == 0x22:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(4, "LD", fmt.Sprintf("($%04X),%s", nn, reg))
	// LD IX/IY,(nn)
	case op == 0x2A:
		nn := uint16(read(addr)) | uint16(read(addr+1))<<8
		return mk(4, "LD", fmt.Sprintf("%s,($%04X)", reg, nn))
	// INC/DEC IX/IY
	case op == 0x23:
		return mk(2, "INC", reg)
	case op == 0x2B:
		return mk(2, "DEC", reg)
	// INC/DEC (IX/IY+d)
	case op == 0x34:
		d := int8(read(addr))
		return mk(3, "INC", fmt.Sprintf("(%s%+d)", reg, d))
	case op == 0x35:
		d := int8(read(addr))
		return mk(3, "DEC", fmt.Sprintf("(%s%+d)", reg, d))
	// LD (IX/IY+d),n
	case op == 0x36:
		d := int8(read(addr))
		n := read(addr + 1)
		return mk(4, "LD", fmt.Sprintf("(%s%+d),$%02X", reg, d, n))
	// LD r,(IX/IY+d) / LD (IX/IY+d),r
	case op >= 0x46 && op <= 0x7E && (op&7) == 6 && op != 0x76:
		d := int8(read(addr))
		r8 := [8]string{"B", "C", "D", "E", "H", "L", "", "A"}
		return mk(3, "LD", fmt.Sprintf("%s,(%s%+d)", r8[(op>>3)&7], reg, d))
	case op >= 0x70 && op <= 0x77 && op != 0x76:
		d := int8(read(addr))
		r8 := [8]string{"B", "C", "D", "E", "H", "L", "", "A"}
		return mk(3, "LD", fmt.Sprintf("(%s%+d),%s", reg, d, r8[op&7]))
	// ALU (IX/IY+d)
	case op >= 0x86 && op <= 0xBE && (op&7) == 6:
		d := int8(read(addr))
		alu := [8]string{"ADD A,", "ADC A,", "SUB ", "SBC A,", "AND ", "XOR ", "OR ", "CP "}
		return mk(3, alu[(op-0x86)>>3], fmt.Sprintf("(%s%+d)", reg, d))
	// PUSH/POP IX/IY
	case op == 0xE1:
		return mk(2, "POP", reg)
	case op == 0xE5:
		return mk(2, "PUSH", reg)
	// JP (IX/IY)
	case op == 0xE9:
		return mk(2, "JP", fmt.Sprintf("(%s)", reg))
	// LD SP,IX/IY
	case op == 0xF9:
		return mk(2, "LD", fmt.Sprintf("SP,%s", reg))
	// EX (SP),IX/IY
	case op == 0xE3:
		return mk(2, "EX", fmt.Sprintf("(SP),%s", reg))
	// DDCB/FDCB
	case op == 0xCB:
		d := int8(read(addr))
		cbop := read(addr + 1)
		r8 := [8]string{"B", "C", "D", "E", "H", "L", fmt.Sprintf("(%s%+d)", reg, d), "A"}
		rot := [8]string{"RLC", "RRC", "RL", "RR", "SLA", "SRA", "SLL", "SRL"}
		b := bytes(read, start, 4)
		switch {
		case cbop < 0x40:
			return DisassembledLine{start, b, rot[cbop>>3], r8[cbop&7]}
		case cbop < 0x80:
			return DisassembledLine{start, b, "BIT", fmt.Sprintf("%d,%s", (cbop>>3)&7, r8[cbop&7])}
		case cbop < 0xC0:
			return DisassembledLine{start, b, "RES", fmt.Sprintf("%d,%s", (cbop>>3)&7, r8[cbop&7])}
		default:
			return DisassembledLine{start, b, "SET", fmt.Sprintf("%d,%s", (cbop>>3)&7, r8[cbop&7])}
		}
	}
	return mk(2, fmt.Sprintf("DB $%02X,$%02X", read(start), op), "")
}
