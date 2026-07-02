package z80

import (
	"testing"
)

// Canonical Z80N (Spectrum Next extended) opcode T-state cross-check.
// Each entry is measured end-to-end through
// StepInstruction on a VariantZ80N CPU and compared to the documented
// total from the SpecNext wiki Extended_Z80_instruction_set page.
// This catches prefix-fetch double-counting that a literal read of
// the `c.tstates += N` lines cannot.
//
// Block-repeat opcodes (LDIRX/LDDRX/LDPIRX) are listed twice: once
// with BC=1 (final iteration, 16T) and once with BC=2 (repeating
// iteration, 21T + PC rewind).

func runZ80NTiming(t *testing.T, bytes []byte, setup func(*CPU, memoryWriter)) uint64 {
	t.Helper()
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	cpu.Variant = VariantZ80N
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

func TestZ80N_TimingTable(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		setup func(*CPU, memoryWriter)
		want  uint64
	}{
		// Arithmetic — all 8T except the nn-immediate adds (16T).
		{"MUL D,E", []byte{0xED, 0x30}, nil, 8},
		{"ADD HL,A", []byte{0xED, 0x31}, nil, 8},
		{"ADD DE,A", []byte{0xED, 0x32}, nil, 8},
		{"ADD BC,A", []byte{0xED, 0x33}, nil, 8},
		{"ADD HL,nn", []byte{0xED, 0x34, 0x34, 0x12}, nil, 16},
		{"ADD DE,nn", []byte{0xED, 0x35, 0x34, 0x12}, nil, 16},
		{"ADD BC,nn", []byte{0xED, 0x36, 0x34, 0x12}, nil, 16},
		// Bit / shift — all 8T; TEST n is 11T (extra operand byte).
		{"SWAPNIB", []byte{0xED, 0x23}, nil, 8},
		{"MIRROR A", []byte{0xED, 0x24}, nil, 8},
		{"TEST n", []byte{0xED, 0x27, 0x55}, nil, 11},
		{"BSLA DE,B", []byte{0xED, 0x28}, nil, 8},
		{"BSRA DE,B", []byte{0xED, 0x29}, nil, 8},
		{"BSRL DE,B", []byte{0xED, 0x2A}, nil, 8},
		{"BSRF DE,B", []byte{0xED, 0x2B}, nil, 8},
		{"BRLC DE,B", []byte{0xED, 0x2C}, nil, 8},
		{"SETAE", []byte{0xED, 0x95}, nil, 8},
		// System group.
		{"PUSH nn", []byte{0xED, 0x8A, 0x12, 0x34}, nil, 23},
		{"OUTINB", []byte{0xED, 0x90}, nil, 16},
		{"NEXTREG r,n", []byte{0xED, 0x91, 0x07, 0x03}, nil, 20},
		{"NEXTREG r,A", []byte{0xED, 0x92, 0x07}, nil, 17},
		{"PIXELDN", []byte{0xED, 0x93}, nil, 8},
		{"PIXELAD", []byte{0xED, 0x94}, nil, 8},
		{"JP (C)", []byte{0xED, 0x98}, nil, 13},
		// Block group — non-repeating.
		{"LDIX", []byte{0xED, 0xA4}, setBC(1), 16},
		{"LDWS", []byte{0xED, 0xA5}, nil, 14},
		{"LDDX", []byte{0xED, 0xAC}, setBC(1), 16},
		// Block group — repeat variants, final iteration (BC→0 = 16T).
		{"LDIRX final", []byte{0xED, 0xB4}, setBC(1), 16},
		{"LDDRX final", []byte{0xED, 0xBC}, setBC(1), 16},
		{"LDPIRX final", []byte{0xED, 0xB7}, setBC(1), 16},
		// Block group — repeat variants, repeating iteration (BC>0 = 21T).
		{"LDIRX repeat", []byte{0xED, 0xB4}, setBC(2), 21},
		{"LDDRX repeat", []byte{0xED, 0xBC}, setBC(2), 21},
		{"LDPIRX repeat", []byte{0xED, 0xB7}, setBC(2), 21},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runZ80NTiming(t, c.bytes, c.setup)
			if got != c.want {
				t.Errorf("%s: %d T, want %d T", c.name, got, c.want)
			}
		})
	}
}

// setBC returns a setup that primes BC so block-op repeat vs final
// paths can be selected. Points HL/DE at writable high RAM so the
// copy doesn't fault.
func setBC(v uint16) func(*CPU, memoryWriter) {
	return func(c *CPU, _ memoryWriter) {
		c.setBC(v)
		c.setHL(0x9000)
		c.setDE(0x9100)
	}
}

// TestZ80N_TimingNeedsVariant confirms the Z80N timings only apply on
// a VariantZ80N CPU — on a plain Z80 the ED byte is an unknown
// (NONI-style) ED opcode, which must NOT decode as a Z80N op.
func TestZ80N_TimingNeedsVariant(t *testing.T) {
	cpu, mem := createTestCPU() // plain Z80, not Z80N
	defer cleanupTestROMs("test_roms_z80")
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x30) // would be MUL on Z80N
	cpu.D, cpu.E = 0x10, 0x10
	cpu.StepInstruction()
	// On a plain Z80, ED $30 is not MUL: DE must be unchanged.
	if cpu.de() == 0x0100 {
		t.Error("ED $30 decoded as MUL on a non-Z80N CPU")
	}
}
