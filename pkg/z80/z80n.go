package z80

// executeZ80NEDInstruction dispatches the ZX Spectrum Next's extended
// ED-prefixed opcodes. Returns true if the opcode was handled and the
// caller should stop dispatching; false if the opcode is not a
// Z80N extension (and the caller should treat it as an unknown ED
// byte per existing behaviour).
//
// All Z80N opcodes are guarded by Variant==VariantZ80N at the caller
// site (executeEDInstruction's default branch); this function does
// not re-check.
//
// T-state counts are taken from the official wiki
// (https://wiki.specnext.dev/Extended_Z80_instruction_set). Most
// opcodes preserve all flags; the few that affect flags say so
// explicitly. Operand-fetch contention is handled by the existing
// fetch / fetch16 helpers.
func (c *CPU) executeZ80NEDInstruction(opcode byte) bool {
	switch opcode {

	// --- Arithmetic group ---
	// All preserve flags.

	case 0x30: // MUL D,E    DE := D * E (unsigned 8x8 -> 16)
		c.setDE(uint16(c.D) * uint16(c.E))
		c.tstates += 8
		return true
	case 0x31: // ADD HL,A   HL += unsigned A
		c.setHL(c.hl() + uint16(c.A))
		c.tstates += 8
		return true
	case 0x32: // ADD DE,A   DE += unsigned A
		c.setDE(c.de() + uint16(c.A))
		c.tstates += 8
		return true
	case 0x33: // ADD BC,A   BC += unsigned A
		c.setBC(c.bc() + uint16(c.A))
		c.tstates += 8
		return true
	case 0x34: // ADD HL,nn  16-bit immediate add
		c.setHL(c.hl() + c.fetch16())
		c.tstates += 16
		return true
	case 0x35: // ADD DE,nn
		c.setDE(c.de() + c.fetch16())
		c.tstates += 16
		return true
	case 0x36: // ADD BC,nn
		c.setBC(c.bc() + c.fetch16())
		c.tstates += 16
		return true

	// --- Bit / shift group ---

	case 0x23: // SWAPNIB    A = high<->low nibble of A
		c.A = (c.A << 4) | (c.A >> 4)
		c.tstates += 8
		return true
	case 0x24: // MIRROR A   A = bit-reverse of A
		a := c.A
		a = ((a & 0xF0) >> 4) | ((a & 0x0F) << 4)
		a = ((a & 0xCC) >> 2) | ((a & 0x33) << 2)
		a = ((a & 0xAA) >> 1) | ((a & 0x55) << 1)
		c.A = a
		c.tstates += 8
		return true
	case 0x27: // TEST n     A & n -> flags only; A preserved
		n := c.readOperand()
		r := c.A & n
		// Same flag effects as AND: S/Z from result, H=1,
		// P/V=parity, N=0, C=0. A is NOT modified.
		c.F = FLAG_H | c.sz53Table[r] | c.parityTable[r]
		c.tstates += 11
		return true
	case 0x28: // BSLA DE,B  barrel shift left, B mod 32
		c.setDE(barrelShiftLeft(c.de(), c.B))
		c.tstates += 8
		return true
	case 0x29: // BSRA DE,B  barrel shift right arithmetic
		c.setDE(barrelShiftRightArith(c.de(), c.B))
		c.tstates += 8
		return true
	case 0x2A: // BSRL DE,B  barrel shift right logical
		c.setDE(barrelShiftRightLogical(c.de(), c.B))
		c.tstates += 8
		return true
	case 0x2B: // BSRF DE,B  barrel shift right, fill with 1s
		c.setDE(barrelShiftRightFill(c.de(), c.B))
		c.tstates += 8
		return true
	case 0x2C: // BRLC DE,B  barrel rotate left circular, B mod 16
		c.setDE(barrelRotateLeftCircular(c.de(), c.B))
		c.tstates += 8
		return true
	case 0x95: // SETAE      A = $80 >> (E & 7); per-pixel mask (leftmost
		// pixel E&7==0 -> bit 7, rightmost E&7==7 -> bit 0). See setae_test.go.
		c.A = 0x80 >> (c.E & 7)
		c.tstates += 8
		return true

	// --- Block group ---
	//
	// LDIX/LDDX/LDIRX/LDDRX/LDPIRX implement "transparent blits":
	// the source byte is skipped (not written to destination) when
	// it equals register A. This is the canonical sprite-paste
	// idiom on the Next — pixels matching the colour key are
	// treated as transparent.
	//
	// Note the directions: LDIX advances DE forward; LDDX advances
	// DE backward but still advances HL forward (the "D" in LDDX
	// names the destination-decrement only, not source-decrement
	// like classic LDD). LDWS is a pure word-step helper for
	// bitmap-screen advance — INC L, INC D (8-bit each, so within
	// a 256-byte row/page).
	//
	// Flag effects follow classic LDI/LDD for the non-repeating
	// variants: P/V from BC after decrement, N and H cleared, S/Z
	// unchanged for the value-copy. Repeating variants leave flags
	// at their final-iteration state.

	case 0xA4: // LDIX     copy with key=A skip; HL++, DE++, BC--
		c.ldix()
		c.tstates += 16
		return true
	case 0xA5: // LDWS     copy; L++, D++  (8-bit, intra-row)
		c.ldws()
		c.tstates += 14
		return true
	case 0xAC: // LDDX     copy with key=A skip; HL++, DE--, BC--
		c.lddx()
		c.tstates += 16
		return true
	case 0xB4: // LDIRX    LDIX repeat
		c.ldix()
		if c.bc() != 0 {
			c.PC -= 2 // re-execute ED B4
			c.tstates += 21
		} else {
			c.tstates += 16
		}
		return true
	case 0xBC: // LDDRX    LDDX repeat
		c.lddx()
		if c.bc() != 0 {
			c.PC -= 2
			c.tstates += 21
		} else {
			c.tstates += 16
		}
		return true
	case 0xB7: // LDPIRX   pattern fill, repeat
		c.ldpirx()
		if c.bc() != 0 {
			c.PC -= 2
			c.tstates += 21
		} else {
			c.tstates += 16
		}
		return true

	// --- System group ---
	//
	// NEXTREG writes go through the CPU's NextRegSink. A nil sink
	// is treated as a no-op: this lets pkg/z80 unit-test the
	// dispatch path without dragging in the Next bus.

	case 0x8A: // PUSH nn  big-endian operand: hi byte first
		hi := c.readOperand()
		lo := c.readOperand()
		c.push((uint16(hi) << 8) | uint16(lo))
		c.tstates += 23
		return true
	case 0x90: // OUTINB    output (HL) to (BC); HL++; B unchanged
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.mem.Read(c.hl()))
		c.setHL(c.hl() + 1)
		c.tstates += 16
		return true
	case 0x91: // NEXTREG r,n   write n to NextReg r
		reg := c.readOperand()
		val := c.readOperand()
		if c.NextRegs != nil {
			c.NextRegs.WriteReg(reg, val)
		}
		c.tstates += 20
		return true
	case 0x92: // NEXTREG r,A   write A to NextReg r
		reg := c.readOperand()
		if c.NextRegs != nil {
			c.NextRegs.WriteReg(reg, c.A)
		}
		c.tstates += 17
		return true
	case 0x93: // PIXELDN   advance HL one pixel row in screen layout
		c.pixeldn()
		c.tstates += 8
		return true
	case 0x94: // PIXELAD   HL = screen address of pixel (D=Y, E=X)
		c.pixelad()
		c.tstates += 8
		return true
	case 0x98: // JP (C)    PC = (PC & 0xC000) | (BC & 0x3FFF)
		c.PC = (c.PC & 0xC000) | (c.bc() & 0x3FFF)
		c.tstates += 13
		return true
	}
	return false
}

// pixeldn advances HL one pixel row in the ZX Spectrum bitmap-screen
// address layout. The Spectrum splits Y across H bits 2-0 (pixel
// within char-row), L bits 7-5 (char-row within group) and H bits
// 4-3 (row group). Incrementing Y is a three-stage cascade.
func (c *CPU) pixeldn() {
	h, l := c.H, c.L
	// The Z80N core (t80n.vhd PIXELDN) extracts the 8-bit Y coordinate from
	// the ZX screen-address bitfields — Y = H[4:3] ++ L[7:5] ++ H[2:0] —
	// increments it, and redistributes it, preserving H[7:5] and L[4:0].
	// (For valid screen addresses this equals the classic inc-H algorithm;
	// at the Y-wrap boundary only this bitfield form matches the FPGA.)
	y := (uint16(h&0x18) << 3) | (uint16(l&0xE0) >> 2) | uint16(h&0x07)
	y = (y + 1) & 0xFF
	c.H = (h & 0xE0) | byte((y&0xC0)>>3) | byte(y&0x07)
	c.L = byte((y&0x38)<<2) | (l & 0x1F)
}

// pixelad sets HL to the Spectrum screen address for pixel (D=Y,
// E=X). Constant screen base 0x4000; address layout per pixeldn.
func (c *CPU) pixelad() {
	y, x := c.D, c.E
	addr := uint16(0x4000)
	addr |= (uint16(y) & 0xC0) << 5 // Y bits 7-6 -> A12-A11
	addr |= (uint16(y) & 0x07) << 8 // Y bits 2-0 -> A10-A8
	addr |= (uint16(y) & 0x38) << 2 // Y bits 5-3 -> A7-A5
	addr |= uint16(x) >> 3          // X bits 7-3 -> A4-A0
	c.setHL(addr)
}

// ldix performs one step of the LDIX block: copy (HL) to (DE)
// unless the source byte equals A; then HL++, DE++, BC--. Flags
// follow classic LDI: N=0, H=0, P/V from BC after decrement, S/Z
// preserved, F3/F5 from the (val+A) trick LDI uses.
func (c *CPU) ldix() {
	val := c.mem.Read(c.hl())
	if val != c.A {
		c.mem.Write(c.de(), val)
	}
	c.setHL(c.hl() + 1)
	c.setDE(c.de() + 1)
	c.setBC(c.bc() - 1)
	c.F &^= FLAG_N | FLAG_H | FLAG_PV
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	// F3/F5 follow the classic LDI rule: bits from (val + A).
	t := val + c.A
	c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | (t & FLAG_F3) | ((t << 4) & FLAG_F5)
}

// lddx is LDIX with destination decrement. Source (HL) still
// advances forward; only DE decrements. Same flag pattern as ldix.
func (c *CPU) lddx() {
	val := c.mem.Read(c.hl())
	if val != c.A {
		c.mem.Write(c.de(), val)
	}
	c.setHL(c.hl() + 1)
	c.setDE(c.de() - 1)
	c.setBC(c.bc() - 1)
	c.F &^= FLAG_N | FLAG_H | FLAG_PV
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	t := val + c.A
	c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | (t & FLAG_F3) | ((t << 4) & FLAG_F5)
}

// ldws copies (HL) to (DE), then 8-bit increments L and D. Used to
// step one byte along the Spectrum bitmap-screen layout where one
// "down a pixel row" is a +1 to D (the high byte).
//
// Flag effects (per the SpecNext wiki Extended_Z80_instruction_set
// page): S, Z, F3, F5
// derive from the source byte; H, P/V, N are cleared; C is
// preserved. This is the same shape as a load that "tests" the
// transferred value, which matches the common LDWS-in-a-loop idiom.
func (c *CPU) ldws() {
	val := c.mem.Read(c.hl())
	c.mem.Write(c.de(), val)
	c.L++
	c.D++
	c.F = (c.F & FLAG_C) | c.sz53Table[val]
}

// ldpirx copies a pattern byte to (DE). The source address is
// (HL & 0xFFF8) | (DE & 7) — i.e. HL points at the start of an
// 8-byte pattern and DE's low 3 bits index into it. Skip on
// (pattern byte) == A. DE++, BC--. HL is preserved.
//
// Flag effects match LDIRX (LDI-style): N=0, H=0, P/V from BC
// after decrement, S/Z preserved, F3/F5 from (val + A).
func (c *CPU) ldpirx() {
	srcAddr := (c.hl() & 0xFFF8) | (c.de() & 7)
	val := c.mem.Read(srcAddr)
	if val != c.A {
		c.mem.Write(c.de(), val)
	}
	c.setDE(c.de() + 1)
	c.setBC(c.bc() - 1)
	c.F &^= FLAG_N | FLAG_H | FLAG_PV
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	t := val + c.A
	c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | (t & FLAG_F3) | ((t << 4) & FLAG_F5)
}

// Barrel-shift helpers. The Next's FPGA implements all four shift
// variants and the rotate as a single 5-bit-counter shifter, so the
// shift count is `B mod 32` (mod 16 for the rotate). All preserve
// flags.

func barrelShiftLeft(de uint16, b byte) uint16 {
	shift := uint(b & 31)
	if shift >= 16 {
		return 0
	}
	return de << shift
}

func barrelShiftRightLogical(de uint16, b byte) uint16 {
	shift := uint(b & 31)
	if shift >= 16 {
		return 0
	}
	return de >> shift
}

func barrelShiftRightArith(de uint16, b byte) uint16 {
	shift := uint(b & 31)
	sign := de & 0x8000
	if shift == 0 {
		return de
	}
	if shift >= 16 {
		if sign != 0 {
			return 0xFFFF
		}
		return 0
	}
	out := de >> shift
	if sign != 0 {
		out |= ^uint16(0) << (16 - shift)
	}
	return out
}

func barrelShiftRightFill(de uint16, b byte) uint16 {
	shift := uint(b & 31)
	if shift == 0 {
		return de
	}
	if shift >= 16 {
		return 0xFFFF
	}
	return (de >> shift) | (^uint16(0) << (16 - shift))
}

func barrelRotateLeftCircular(de uint16, b byte) uint16 {
	shift := uint(b & 15)
	if shift == 0 {
		return de
	}
	return (de << shift) | (de >> (16 - shift))
}
