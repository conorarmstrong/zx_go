package z80

import (
	"testing"
)

// Per-opcode I/O contention. Each I/O opcode routes its
// port access through memory.ContendPort, so an access to a
// contended ULA port (even port, address in $4000-$7FFF) must cost
// MORE T-states when the CPU is positioned inside the contended
// display window than outside it. This proves the contention is
// applied per-opcode, not just available as a helper.
//
// The fixture wires real Memory (createTestCPU shares its T-state
// counter, enabling contention), so positioning cpu.tstates selects
// the display vs border region.

func ioContentionCost(t *testing.T, bytes []byte, startT uint64, setup func(*CPU)) uint64 {
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

func TestIOContention_PerOpcode(t *testing.T) {
	// A=$40 makes IN A,(n)/OUT (n),A form port $40nn (contended bank
	// 5). BC=$40FE makes the ED (C) ops + block I/O hit a contended
	// ULA port too.
	const displayT = 14335 // first contended display T-state
	const borderT = 100000 // well past display → no contention

	cases := []struct {
		name  string
		bytes []byte
		setup func(*CPU)
	}{
		{"IN A,(n)", []byte{0xDB, 0xFE}, func(c *CPU) { c.A = 0x40 }},
		{"OUT (n),A", []byte{0xD3, 0xFE}, func(c *CPU) { c.A = 0x40 }},
		{"IN A,(C)", []byte{0xED, 0x78}, func(c *CPU) { c.setBC(0x40FE) }},
		{"OUT (C),A", []byte{0xED, 0x79}, func(c *CPU) { c.setBC(0x40FE); c.A = 0x55 }},
		{"INI", []byte{0xED, 0xA2}, func(c *CPU) { c.setBC(0x40FE); c.setHL(0x9000) }},
		{"OUTI", []byte{0xED, 0xA3}, func(c *CPU) { c.setBC(0x40FE); c.setHL(0x9000) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inside := ioContentionCost(t, c.bytes, displayT, c.setup)
			outside := ioContentionCost(t, c.bytes, borderT, c.setup)
			if inside <= outside {
				t.Errorf("%s: in-display=%d T not > border=%d T — contention not applied per-opcode",
					c.name, inside, outside)
			}
		})
	}
}

// TestIOContention_UncontendedNoExtra confirms an access to a
// non-contended, non-ULA port (odd port, address outside $4000-$7FFF)
// costs the SAME in display and border regions — no spurious
// contention.
func TestIOContention_UncontendedNoExtra(t *testing.T) {
	// A=$00, n=$FF → port $00FF: odd (non-ULA) + non-contended.
	setup := func(c *CPU) { c.A = 0x00 }
	inside := ioContentionCost(t, []byte{0xDB, 0xFF}, 14335, setup)
	outside := ioContentionCost(t, []byte{0xDB, 0xFF}, 100000, setup)
	if inside != outside {
		t.Errorf("uncontended IN A,(n): in-display=%d T != border=%d T — spurious contention",
			inside, outside)
	}
}
