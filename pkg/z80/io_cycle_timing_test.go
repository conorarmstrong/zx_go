package z80

import "testing"

// I/O machine-cycle timing on a ULA port, against Fuse (periph.c writeport /
// readport, peripherals/ula.c ula_contend_port_early/late). The I/O cycle is
// 4 T. A write reaches the port 1 T into it, after the early contention; a
// read samples 3 T into it, after the late contention.
//
// Where the cycle starts depends on the opcode: OUT/IN (n) after M1 + the
// operand read (+7); the ED (C) forms after two M1s (+8); INI/IND after two
// M1s and one internal T at I:R (+9); OUTI/OUTD after that plus the (HL)
// read (+12).
//
// The border colour latches on the write, so an error here moves every
// border change sideways on the screen (and a 4 T error per OUT slows every
// beeper routine).

type ioTimingULA struct {
	*mockULA
	t  *uint64
	at uint64 // T-state counter when the port was accessed
}

func (u *ioTimingULA) WritePort(addr uint16, val byte) { u.at = *u.t }

func (u *ioTimingULA) ReadPort(addr uint16) (byte, bool) {
	u.at = *u.t
	return 0xFF, true
}

// ioTiming runs one instruction from startT and returns the offset of the
// port access and the instruction's total T-states.
func ioTiming(t *testing.T, code []byte, startT uint64, setup func(*CPU)) (access, total uint64) {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	u := &ioTimingULA{mockULA: newMockULA(), t: &cpu.tstates}
	cpu.ula = u
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	for i, b := range code {
		mem.Write(uint16(0x8000+i), b)
	}
	if setup != nil {
		setup(cpu)
	}
	cpu.tstates = startT
	cpu.StepInstruction()
	return u.at - startT, cpu.tstates - startT
}

func TestULAPortIOCycleTiming(t *testing.T) {
	const borderT = 100000 // outside the contention window

	// Port $00FE: a ULA port whose high byte is not in contended memory.
	a0 := func(c *CPU) { c.A = 0x00 }
	bc := func(c *CPU) { c.setBC(0x01FE); c.setHL(0x9000) }
	bc2 := func(c *CPU) { c.setBC(0x02FE); c.setHL(0x9000) }

	cases := []struct {
		name          string
		code          []byte
		setup         func(*CPU)
		access, total uint64
	}{
		{"OUT (n),A", []byte{0xD3, 0xFE}, a0, 8, 11},
		{"IN A,(n)", []byte{0xDB, 0xFE}, a0, 10, 11},
		{"OUT (C),A", []byte{0xED, 0x79}, bc, 9, 12},
		{"OUT (C),0", []byte{0xED, 0x71}, bc, 9, 12},
		{"IN B,(C)", []byte{0xED, 0x40}, bc, 11, 12},
		{"IN F,(C)", []byte{0xED, 0x70}, bc, 11, 12},
		{"INI", []byte{0xED, 0xA2}, bc, 12, 16},
		{"IND", []byte{0xED, 0xAA}, bc, 12, 16},
		{"OUTI", []byte{0xED, 0xA3}, bc2, 13, 16},
		{"OUTD", []byte{0xED, 0xAB}, bc2, 13, 16},
		{"INIR repeating", []byte{0xED, 0xB2}, bc2, 12, 21},
		{"OTIR repeating", []byte{0xED, 0xB3}, bc2, 13, 21},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			access, total := ioTiming(t, c.code, borderT, c.setup)
			if access != c.access || total != c.total {
				t.Errorf("port access at +%d, total %d T; want +%d, %d T",
					access, total, c.access, c.total)
			}
		})
	}
}

// TestULAPortContentionSampledAtIOCycle pins WHEN the ULA-port contention is
// sampled: 1 T into the I/O cycle (Fuse ula_contend_port_late), not at the
// start of the instruction. OUT (n),A from 14327 opens its I/O cycle at 14334,
// so the late check lands on 14335, the first contended T-state (delay 6).
func TestULAPortContentionSampledAtIOCycle(t *testing.T) {
	access, total := ioTiming(t, []byte{0xD3, 0xFE}, 14327, func(c *CPU) { c.A = 0 })
	if access != 8 || total != 11+6 {
		t.Errorf("port access at +%d, total %d T; want +8, 17 T", access, total)
	}
}
