package sam

import "github.com/conorarmstrong/zx_go/pkg/z80"

// CyclesPerFrame is the SAM frame length in CPU T-states: 6 MHz / ~50.08 Hz =
// 384 cycles/line × 312 lines. (SimCoupe SAM.h CPU_CYCLES_PER_FRAME.)
const CyclesPerFrame = 119808

// Machine is a SAM Coupé: the shared Z80 core driving the SAM memory map and
// (from Sprint 2) the SAM I/O/ASIC. It implements z80.ULA (ReadPort/WritePort)
// itself, the way pkg/zx8x's Machine does.
type Machine struct {
	Mem *Memory
	CPU *z80.CPU
}

// New builds a SAM machine from the two 16 KB ROM halves. z80.New resets the
// CPU, so PC starts at $0000 with ROM0 paged in (the SAM reset entry).
func New(rom0, rom1 []byte) *Machine {
	m := &Machine{Mem: NewMemory(rom0, rom1)}
	m.CPU = z80.New(m.Mem, m)
	return m
}

// NewFromROM validates + splits a raw 32 KB SAM ROM image and builds the machine.
func NewFromROM(romImage []byte) (*Machine, error) {
	rom0, rom1, err := SplitROM(romImage)
	if err != nil {
		return nil, err
	}
	return New(rom0, rom1), nil
}

// ReadPort/WritePort implement the z80.ULA interface (the SAM I/O space). This
// is a benign stub for Sprint 0 — reads float high, writes are dropped — so the
// CPU can execute. Sprint 2 implements the real low-byte port map (paging,
// BORDER, STATUS, keyboard, line interrupt, SAA, floppy).
func (m *Machine) ReadPort(addr uint16) (byte, bool) { return 0xFF, true }

func (m *Machine) WritePort(addr uint16, val byte) {}

// RunFrame executes one 50 Hz SAM frame.
func (m *Machine) RunFrame() {
	m.CPU.ExecuteFrame(CyclesPerFrame)
}
