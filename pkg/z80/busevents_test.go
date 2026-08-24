package z80

import "testing"

// The IM2 daisy chain is driven by two bus events the CPU has never exposed:
// the interrupt-acknowledge cycle, where the device that won arbitration puts
// its vector on the data bus, and the RETI that tells it the handler is done.
// Without them a peripheral cannot deliver a vectored interrupt or ever release
// its in-service slot.

type busEventMem struct{ b []byte }

func (m *busEventMem) Read(a uint16) byte     { return m.b[a] }
func (m *busEventMem) Write(a uint16, v byte) { m.b[a] = v }
func (m *busEventMem) ContendPort(uint16)     {}

type busEventULA struct{}

func (busEventULA) ReadPort(uint16) (byte, bool) { return 0xFF, true }
func (busEventULA) WritePort(uint16, byte)       {}

func newBusEventCPU() (*CPU, *busEventMem) {
	m := &busEventMem{b: make([]byte, 0x10000)}
	c := New(m, busEventULA{})
	c.PC = 0x8000
	return c, m
}

// o_reti_seen goes high when the chain decodes ED $4D (im2_control.vhd:172-173,
// :234), and the decode at :135 is an exact match on $4D. The mirrors $5D, $6D
// and $7D execute identically on the CPU and are NOT this signal.
//
// That distinguishes it from the existing RETN hook, which fires for every
// mirror of both instructions because the T80N asserts I_RETN for all of them
// and the divMMC wants exactly that.
func TestRETIHookFiresOnlyOnTheExactOpcode(t *testing.T) {
	for _, c := range []struct {
		name string
		op   byte
		want int
	}{
		{"ED 4D, the real RETI", 0x4D, 1},
		{"ED 5D mirror", 0x5D, 0},
		{"ED 6D mirror", 0x6D, 0},
		{"ED 7D mirror", 0x7D, 0},
		{"ED 45, RETN", 0x45, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			cpu, mem := newBusEventCPU()
			seen := 0
			cpu.SetRETISeenHook(func() { seen++ })
			mem.b[0x8000], mem.b[0x8001] = 0xED, c.op
			cpu.SP = 0xFF00
			mem.b[0xFF00], mem.b[0xFF01] = 0x34, 0x12
			cpu.StepInstruction()
			if seen != c.want {
				t.Errorf("RETI hook fired %d times for ED $%02X, want %d", seen, c.op, c.want)
			}
			if cpu.PC != 0x1234 {
				t.Errorf("PC = $%04X, want $1234: the instruction must still execute", cpu.PC)
			}
		})
	}
}

// During the acknowledge cycle the interrupting device puts its vector on the
// data bus, and in IM2 the CPU uses it as the low byte of the handler pointer.
// The ULA's 0xFF is only the default for a machine with nothing on the chain.
func TestINTAckHookSuppliesTheIM2Vector(t *testing.T) {
	cpu, mem := newBusEventCPU()
	cpu.IM, cpu.IFF1 = 2, true
	cpu.I = 0x80
	cpu.A = 0x10         // New() leaves A at $FF, and INC A would wrap it to zero
	mem.b[0x8000] = 0x00 // NOP at the interrupted PC
	cpu.SP = 0xFF00

	// The device presents vector $0A, so the handler pointer is at $800A.
	cpu.SetINTAckHook(func() (byte, bool) { return 0x0A, true })
	mem.b[0x800A], mem.b[0x800B] = 0x78, 0x56
	// INC A at the handler, and NOP everywhere else, so landing one byte out
	// leaves A at zero. Asserting on PC instead would be asserting on how far
	// the step ran after vectoring, which is not what this test is about.
	mem.b[0x5678] = 0x3C

	cpu.IRQPending.Store(true)
	cpu.StepInstructionWithIRQ()

	if cpu.A != 0x11 {
		t.Errorf("A = $%02X after the interrupt, want $11: the handler at $5678 did not "+
			"run, so the vector the device presented was not used as the low byte "+
			"of the IM2 pointer (PC = $%04X)", cpu.A, cpu.PC)
	}
}

// With nothing on the chain, the CPU keeps the ULA's floating $FF: the hook is
// an addition, not a replacement.
func TestIM2FallsBackToTheDefaultVector(t *testing.T) {
	cpu, mem := newBusEventCPU()
	cpu.IM, cpu.IFF1 = 2, true
	cpu.I = 0x80
	cpu.A = 0x10 // New() leaves A at $FF, and INC A would wrap it to zero
	mem.b[0x8000] = 0x00
	cpu.SP = 0xFF00
	mem.b[0x80FF], mem.b[0x8100] = 0x21, 0x43
	mem.b[0x4321] = 0x3C // INC A at the handler

	cpu.IRQPending.Store(true)
	cpu.StepInstructionWithIRQ()

	if cpu.A != 0x11 {
		t.Errorf("A = $%02X, want $11: the handler at $4321 did not run, so the default "+
			"vector $FF was not used (PC = $%04X)", cpu.A, cpu.PC)
	}
}

// A device that is on the chain but not asserting presents nothing, and the
// CPU falls back rather than reading a zero off a device that stayed quiet.
func TestINTAckHookDecliningLeavesTheDefault(t *testing.T) {
	cpu, mem := newBusEventCPU()
	cpu.IM, cpu.IFF1 = 2, true
	cpu.I = 0x80
	cpu.A = 0x10 // New() leaves A at $FF, and INC A would wrap it to zero
	mem.b[0x8000] = 0x00
	cpu.SP = 0xFF00
	mem.b[0x80FF], mem.b[0x8100] = 0x21, 0x43
	mem.b[0x4321] = 0x3C // INC A at the handler

	called := 0
	cpu.SetINTAckHook(func() (byte, bool) { called++; return 0x00, false })
	cpu.IRQPending.Store(true)
	cpu.StepInstructionWithIRQ()

	if called != 1 {
		t.Errorf("the acknowledge hook fired %d times, want 1", called)
	}
	if cpu.A != 0x11 {
		t.Errorf("A = $%02X, want $11: a declining device must leave the default vector "+
			"standing, so the handler at $4321 should have run (PC = $%04X)",
			cpu.A, cpu.PC)
	}
}

// The acknowledge cycle happens in every interrupt mode, not just IM2: the
// chain needs to know its interrupt was taken so the winning device can move
// into its in-service state. Only the VECTOR is IM2-specific.
func TestINTAckHookFiresInIM1Too(t *testing.T) {
	cpu, mem := newBusEventCPU()
	cpu.IM, cpu.IFF1 = 1, true
	mem.b[0x8000] = 0x00
	cpu.SP = 0xFF00

	called := 0
	cpu.SetINTAckHook(func() (byte, bool) { called++; return 0, false })
	cpu.IRQPending.Store(true)
	cpu.StepInstructionWithIRQ()

	if called != 1 {
		t.Errorf("the acknowledge hook fired %d times in IM1, want 1", called)
	}
	if cpu.PC == 0x8000 || cpu.PC == 0x8001 {
		t.Errorf("PC = $%04X: the interrupt was not taken at all", cpu.PC)
	}
}
