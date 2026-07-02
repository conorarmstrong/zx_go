package z80

import "testing"

// Per-access memory contention for the ED block opcodes
// (LDI/LDD/LDIR/LDDR, CPI/CPD/CPIR/CPDR, INI/IND/INIR/INDR,
// OUTI/OUTD/OTIR/OTDR). The block memory accesses ((HL) read,
// (DE) write, (HL) write) route through c.rd/c.wr, so an access to
// contended screen RAM ($4000-$7FFF) inside the display window must
// cost MORE T-states than the same opcode positioned at the border,
// while an access to uncontended RAM ($8000+) costs the same in both.
//
// Modelled on ioContentionCost in io_contention_test.go: createTestCPU
// shares the memory T-state counter (contention enabled), so
// positioning cpu.tstates selects the display vs border region.

func edBlockCost(t *testing.T, bytes []byte, startT uint64, setup func(*CPU)) uint64 {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.MemContend = true // exercise the per-access memory-contention path
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	for i, b := range bytes {
		mem.Write(uint16(0x8000+i), b)
	}
	if setup != nil {
		setup(cpu)
	}
	cpu.tstates = startT
	before := cpu.tstates
	cpu.StepInstruction()
	return cpu.tstates - before
}

func TestMemContention_EDBlock_Contended(t *testing.T) {
	// Inside the contended display window. 14330 is chosen so that the
	// contendable access of every opcode in this group (the early (HL)
	// read of LDI/CPI/OUTI as well as the later (HL) write of INI, which
	// lands ~13 T into the instruction) falls in a non-zero ULA-hold
	// slot — proving per-access contention uniformly. The 8-T contention
	// pattern means a few specific start tstates leave a particular
	// access in a zero-hold slot; 14330 avoids that for all 16 opcodes.
	const displayT = 14330 // inside the contended display window
	const borderT = 100000 // well past display → no contention

	const contended = 0x4100   // screen RAM, contended
	const uncontended = 0x9000 // upper RAM, never contended

	// For each opcode, set up the registers so its contendable memory
	// access (the (HL) read/write or (DE) write) hits `addr`. Block-
	// repeat opcodes use B/BC = 1 so this is the single/final iteration
	// (no repeat tail), keeping the comparison about the data access.
	cases := []struct {
		name  string
		bytes []byte
		setup func(*CPU, uint16)
	}{
		// LDI/LDD: read (HL), write (DE). Point BOTH at addr.
		{"LDI", []byte{0xED, 0xA0}, func(c *CPU, a uint16) { c.setHL(a); c.setDE(a); c.setBC(1) }},
		{"LDD", []byte{0xED, 0xA8}, func(c *CPU, a uint16) { c.setHL(a); c.setDE(a); c.setBC(1) }},
		{"LDIR", []byte{0xED, 0xB0}, func(c *CPU, a uint16) { c.setHL(a); c.setDE(a); c.setBC(1) }},
		{"LDDR", []byte{0xED, 0xB8}, func(c *CPU, a uint16) { c.setHL(a); c.setDE(a); c.setBC(1) }},
		// CPI/CPD: read (HL).
		{"CPI", []byte{0xED, 0xA1}, func(c *CPU, a uint16) { c.setHL(a); c.setBC(1) }},
		{"CPD", []byte{0xED, 0xA9}, func(c *CPU, a uint16) { c.setHL(a); c.setBC(1) }},
		{"CPIR", []byte{0xED, 0xB1}, func(c *CPU, a uint16) { c.setHL(a); c.setBC(1) }},
		{"CPDR", []byte{0xED, 0xB9}, func(c *CPU, a uint16) { c.setHL(a); c.setBC(1) }},
		// INI/IND: write (HL). Port is odd (non-ULA) so only the memory
		// write contributes contention difference.
		{"INI", []byte{0xED, 0xA2}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"IND", []byte{0xED, 0xAA}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"INIR", []byte{0xED, 0xB2}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"INDR", []byte{0xED, 0xBA}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		// OUTI/OUTD: read (HL). Port odd (non-ULA).
		{"OUTI", []byte{0xED, 0xA3}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"OUTD", []byte{0xED, 0xAB}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"OTIR", []byte{0xED, 0xB3}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
		{"OTDR", []byte{0xED, 0xBB}, func(c *CPU, a uint16) { c.setHL(a); c.B = 1; c.C = 0xFF }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Contended target: display > border.
			inside := edBlockCost(t, tc.bytes, displayT, func(c *CPU) { tc.setup(c, contended) })
			outside := edBlockCost(t, tc.bytes, borderT, func(c *CPU) { tc.setup(c, contended) })
			if inside <= outside {
				t.Errorf("%s contended addr: in-display=%d T not > border=%d T — per-access contention not applied",
					tc.name, inside, outside)
			}

			// Uncontended target: display == border (no spurious hold).
			inU := edBlockCost(t, tc.bytes, displayT, func(c *CPU) { tc.setup(c, uncontended) })
			outU := edBlockCost(t, tc.bytes, borderT, func(c *CPU) { tc.setup(c, uncontended) })
			if inU != outU {
				t.Errorf("%s uncontended addr: in-display=%d T != border=%d T — spurious contention",
					tc.name, inU, outU)
			}
		})
	}
}
