package z80

import "testing"

// At 28 MHz the Next's SRAM costs every memory READ cycle one wait state, not
// only the opcode fetch: zxnext.vhd:3169-3181 pulls sram_wait_n low on each
// read request edge (sram_req_t, or a bank 5 read) when cpu_speed = "11". The
// M1-only form is the line commented out above it. Writes and I/O are not
// waited.
func TestTurbo28MHzEveryMemoryReadWaits(t *testing.T) {
	cost := func(sel byte, prog ...byte) uint64 {
		cpu, mem := createTestCPU()
		cpu.Variant = VariantZ80N
		cpu.SetSpeedSelect(sel)
		cpu.PC = 0x8000
		cpu.setHL(0x9000)
		for i, b := range prog {
			mem.Write(0x8000+uint16(i), b)
		}
		start := cpu.Tstates()
		cpu.executeInstruction()
		return cpu.Tstates() - start
	}
	for _, c := range []struct {
		name      string
		prog      []byte
		base, w28 uint64
	}{
		{"NOP", []byte{0x00}, 4, 5},                       // one fetch
		{"LD A,n", []byte{0x3E, 0x42}, 7, 9},              // fetch, operand
		{"LD A,(HL)", []byte{0x7E}, 7, 9},                 // fetch, data read
		{"LD (HL),A", []byte{0x77}, 7, 8},                 // fetch; the write is not waited
		{"LD HL,(nn)", []byte{0x2A, 0x00, 0x90}, 16, 21},  // fetch, 2 operands, 2 data reads
		{"LD A,(IX+d)", []byte{0xDD, 0x7E, 0x05}, 19, 23}, // 2 fetches, displacement, data read
		{"BIT 0,(HL)", []byte{0xCB, 0x46}, 12, 15},        // 2 fetches, data read
		{"RET", []byte{0xC9}, 10, 13},                     // fetch, 2 stack reads
		{"OUT (n),A", []byte{0xD3, 0xFE}, 11, 13},         // fetch, operand; the I/O cycle is not waited
	} {
		if got := cost(0, c.prog...); got != c.base {
			t.Errorf("%s at 3.5 MHz = %d T, want %d", c.name, got, c.base)
		}
		if got := cost(3, c.prog...); got != c.w28 {
			t.Errorf("%s at 28 MHz = %d T, want %d: one wait per memory read cycle", c.name, got, c.w28)
		}
	}
}

// countingMem records every address the CPU reads.
type countingMem struct {
	Memory
	reads []uint16
}

func (m *countingMem) Read(a uint16) byte {
	m.reads = append(m.reads, a)
	return m.Memory.Read(a)
}

// The wait is charged per read the CPU makes, so a read the implementation
// makes twice where the hardware makes it once would be charged twice. No Z80
// instruction reads one address twice in a single execution, so for every
// opcode in every table, one execution must read each address at most once.
func TestNoInstructionReadsAnAddressTwice(t *testing.T) {
	prefixes := [][]byte{{}, {0xCB}, {0xED}, {0xDD}, {0xFD}, {0xDD, 0xCB, 0x05}, {0xFD, 0xCB, 0x05}}
	for _, pre := range prefixes {
		for op := 0; op < 256; op++ {
			cpu, mem := createTestCPU()
			cm := &countingMem{Memory: mem}
			cpu.mem = cm
			cpu.Variant = VariantZ80N
			cpu.PC = 0x8000
			cpu.SP = 0xA000
			cpu.setHL(0x9000)
			cpu.IX, cpu.IY = 0x9100, 0x9200
			cpu.setBC(2)
			cpu.setDE(0x9300)
			prog := append(append([]byte{}, pre...), byte(op), 0x10, 0x90)
			for i, b := range prog {
				mem.Write(0x8000+uint16(i), b)
			}
			cpu.executeInstruction()
			seen := map[uint16]bool{}
			for _, a := range cm.reads {
				if seen[a] {
					t.Errorf("prefix % X opcode %02X reads $%04X twice in one execution (reads % X)", pre, op, a, cm.reads)
					break
				}
				seen[a] = true
			}
		}
	}
}
