package sam

import "github.com/conorarmstrong/zx_go/pkg/z80"

// CyclesPerFrame is the SAM frame length in CPU T-states: 6 MHz / ~50.08 Hz =
// 384 cycles/line × 312 lines. (SimCoupe SAM.h CPU_CYCLES_PER_FRAME.)
const CyclesPerFrame = 119808

const (
	// samFrameIntTstate is the frame-relative T-state at which the maskable
	// frame interrupt fires — the end of the 192-line display region (line
	// 68+192 = 260 × 384). The exact value only matters for raster-precise
	// effects; the ROM services the INT once per frame wherever it lands.
	samFrameIntTstate = 99840
	// samIntActiveCycles is how long an interrupt is held active in the STATUS
	// register (SimCoupe CPU_CYCLES_INT_ACTIVE).
	samIntActiveCycles = 128
)

// Machine is a SAM Coupé: the shared Z80 core driving the SAM memory map, the
// I/O/ASIC ports, the key matrix and the SAM interrupt model. It implements
// z80.ULA (ReadPort/WritePort) itself, the way pkg/zx8x's Machine does.
type Machine struct {
	Mem *Memory
	CPU *z80.CPU
	Kbd *Keyboard

	border byte     // last BORDER write (colour + MIC + BEEP + SOFF)
	clut   [16]byte // CLUT palette registers (7-bit indices); consumed by Sprint 3
	line   byte     // line-interrupt target line (>=192 = disabled)

	// frameStart is the CPU T-state count at the start of the current frame,
	// captured in RunFrame so the STATUS register can report interrupt lines
	// relative to the frame.
	frameStart uint64
}

// New builds a SAM machine from the two 16 KB ROM halves. z80.New resets the
// CPU (PC=$0000, ROM0 paged in) and the SAM frame interrupt is armed.
func New(rom0, rom1 []byte) *Machine {
	m := &Machine{
		Mem:  NewMemory(rom0, rom1),
		Kbd:  NewKeyboard(),
		line: 0xFF, // line interrupt disabled
	}
	m.CPU = z80.New(m.Mem, m)
	// Maskable frame interrupt as a narrow pulse (the SAM ASIC pulses int_ula
	// once per frame, held ~128 cycles), reusing the shared Z80 timing hooks.
	m.CPU.IntAssertTstate = samFrameIntTstate
	m.CPU.IntPulseTstates = samIntActiveCycles
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

// RunFrame executes one 50 Hz SAM frame.
func (m *Machine) RunFrame() {
	m.frameStart = m.CPU.Tstates()
	m.CPU.ExecuteFrame(CyclesPerFrame)
}

// BorderColour returns the current 4-bit border CLUT index (BORDER bits map
// ((x&0x20)>>2)|(x&7)). The renderer (Sprint 3) resolves it through the CLUT.
func (m *Machine) BorderColour() byte { return (m.border&0x20)>>2 | (m.border & 0x07) }

// CLUT returns the 16 palette registers (7-bit master-palette indices).
func (m *Machine) CLUT() [16]byte { return m.clut }
