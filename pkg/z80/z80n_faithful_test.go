package z80

import "testing"

// These tests pin two behaviours where the Spectrum Next Z80N (T80N FPGA core)
// deviates from documented NMOS, discovered by the core-golden differential
// suite (see z80n_golden_test.go). The expected values are taken from the
// actual core HDL simulation, not another emulator.

// stepOp writes the opcode bytes at $8000 and single-steps one instruction.
func stepOp(c *CPU, mem interface{ Write(uint16, byte) }, op ...byte) {
	c.PC = 0x8000
	for i, b := range op {
		mem.Write(0x8000+uint16(i), b)
	}
	c.StepInstruction()
}

// TestZ80N_BitIndexed_F3F5: on the Z80N core (t80n_alu.vhd "BIT"), the
// undocumented F3/F5 of a BIT test come from the operand value when the
// opcode's reg field != 6, and are CLEARED to 0 when the reg field == 6 (the
// canonical (HL)/(IX+d)/(IY+d) encoding) — unlike NMOS, which contaminates
// them from the address-high byte (MEMPTR). Verified against the FPGA core.
func TestZ80N_BitIndexed_F3F5(t *testing.T) {
	cases := []struct {
		operand byte
		regNot6 byte // expected F3/F5 for a reg!=6 form (operand-based)
	}{
		{0x00, 0},
		{0x08, FLAG_F3},
		{0x20, FLAG_F5},
		{0x28, FLAG_F3 | FLAG_F5},
		{0xFF, FLAG_F3 | FLAG_F5},
	}
	for _, tc := range cases {
		for _, ix := range []bool{true, false} {
			pfx := byte(0xDD)
			set := func(c *CPU, v uint16) { c.IX = v }
			label := "IX"
			if !ix {
				pfx, label = 0xFD, "IY"
				set = func(c *CPU, v uint16) { c.IY = v }
			}

			// reg field == 6 (canonical BIT 0,(I?+0)): F3/F5 must be 0.
			cpu, mem := createZ80NTestCPU()
			set(cpu, 0xA000)
			cpu.F = 0
			mem.Write(0xA000, tc.operand)
			stepOp(cpu, mem, pfx, 0xCB, 0x00, 0x46)
			if got := cpu.F & (FLAG_F3 | FLAG_F5); got != 0 {
				t.Errorf("BIT 0,(%s+0) [reg=6] operand=%#02x: F3/F5 = %#02x, want 0", label, tc.operand, got)
			}

			// reg field == 0 (undocumented dup): F3/F5 from the operand.
			cpu, mem = createZ80NTestCPU()
			set(cpu, 0xA000)
			cpu.F = 0
			mem.Write(0xA000, tc.operand)
			stepOp(cpu, mem, pfx, 0xCB, 0x00, 0x40)
			if got := cpu.F & (FLAG_F3 | FLAG_F5); got != tc.regNot6 {
				t.Errorf("BIT 0,(%s+0) [reg=0] operand=%#02x: F3/F5 = %#02x, want %#02x", label, tc.operand, got, tc.regNot6)
			}
		}
	}
}

// TestZ80N_PixelDn_Bitfield: PIXELDN extracts the 8-bit Y coordinate from the
// ZX screen-address bitfields (H[4:3] ++ L[7:5] ++ H[2:0]), increments it, and
// redistributes it — preserving H[7:5] and L[4:0]. For valid screen addresses
// this matches the classic inc-H algorithm; at the wrap boundary it does not,
// and only the bitfield form is faithful to the core (t80n.vhd PIXELDN).
func TestZ80N_PixelDn_Bitfield(t *testing.T) {
	cases := []struct{ in, want uint16 }{
		{0x4000, 0x4100}, // top-left, +1 pixel row
		{0x47E0, 0x4800}, // pixel-row wrap into next char row
		{0x7FFF, 0x601F}, // full wrap (core: Y wraps, H[7:5] preserved)
		{0xFFFF, 0xE01F},
	}
	for _, tc := range cases {
		cpu, mem := createZ80NTestCPU()
		cpu.setHL(tc.in)
		stepOp(cpu, mem, 0xED, 0x93)
		if cpu.HL() != tc.want {
			t.Errorf("PIXELDN(%04X) = %04X, want %04X", tc.in, cpu.HL(), tc.want)
		}
	}
}
