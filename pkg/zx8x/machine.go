package zx8x

import (
	"image"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Timing constants (derived from the documented ZX80/ZX81 hardware): the CPU runs at
// 3.25 MHz, each TV scanline is 207 T-states, and a 50 Hz frame is ~312 lines.
const (
	lineTStates  = 207
	FrameTStates = lineTStates * 312
)

// Machine is a Sinclair ZX80 or ZX81. Unlike the Spectrum there is no
// frame-buffer ULA: the Z80 generates the picture itself. During an M1 fetch
// from $8000+, the ULA forces a NOP onto the bus and uses the fetched byte as a
// character code, reading that glyph's bitmap (addressed via the I register and
// the scanline within the character row) and shifting it onto the current TV
// line. A maskable interrupt fires once per scanline (un-halting the display
// loop's per-line HALT) and, on the ZX81 in SLOW mode, an NMI generator pulses
// once per line to pace the borders.
type Machine struct {
	CPU    *z80.CPU
	Mem    *memory.Memory
	KB     *keyboard.Keyboard
	Screen *Screen
	model  roms.SpectrumModel

	// Video position, advanced by the real display structure: characters move
	// the column right; each display-file HALT ($76) ends a scanline (column
	// resets, row advances). The single leading sync HALT before line 0 is not
	// counted (see `started`). Keying off HALTs — not the T-state counter —
	// keeps every character's 8 pixel rows in the same cell.
	beamX   int  // character column within the current scanline
	beamY   int  // pixel scanline within the display (0..191)
	started bool // a video character has been drawn since the last VSYNC

	// Frame/sync state.
	vsync bool   // VSYNC active (IN $FE … next OUT)
	nmiOn bool   // ZX81 SLOW-mode NMI generator enabled
	baseT uint64 // CPU T-state at the start of the current frame
	line  int    // scanlines elapsed this frame (INT/NMI clock)

	front *image.RGBA // last fully-presented frame
	prevR byte        // previous R, for the R-bit-6 INT edge
}

// NewMachine constructs a ZX80/ZX81 (model must be roms.ModelZX80 or
// roms.ModelZX81). romPath is the ROM search directory ("" = embedded only).
func NewMachine(model roms.SpectrumModel, romPath string) (*Machine, error) {
	mem, err := memory.New(romPath, model)
	if err != nil {
		return nil, err
	}
	m := &Machine{
		Mem:    mem,
		KB:     keyboard.New(),
		Screen: NewScreen(NewCharGen(nil)), // live path reads bitmaps via the I register
		model:  model,
	}
	m.KB.UseZX8xLayout()

	cpu := z80.New(mem, m)
	cpu.FrameIntDisabled = true  // the ZX8x drives its own INT, not a frame INT
	cpu.HaltWakeOnInt = true     // INT pulse wakes the display loop's HALT
	cpu.RefreshDuringHalt = true // R counts during HALT so /INT (R bit 6) fires
	cpu.M1FetchHook = m.onM1Fetch
	m.CPU = cpu
	return m, nil
}

// onM1Fetch implements the CPU-generated display: an M1 fetch from the upper
// 32K (A15 = 1) with bit 6 clear is turned into a NOP while its byte is drawn as
// a character. Position is advanced by the display structure itself — column on
// each character, row on each HALT — so a character's 8 pixel rows always land
// in the same cell.
func (m *Machine) onM1Fetch(pc uint16, op byte) byte {
	if pc&0x8000 == 0 || m.CPU.Halted {
		return op
	}
	if op&0x40 != 0 {
		// A HALT ($76) ends a display scanline: reset the column and (once real
		// content has started) advance the row. The single leading sync HALT
		// before line 0 must not advance the row.
		if op == 0x76 {
			if m.started {
				m.beamY++
			}
			m.beamX = 0
		}
		return op
	}
	code := op & 0x3F
	addr := uint16(m.CPU.I)<<8 | uint16(code)<<3 | uint16(m.beamY&7)
	bits := m.Mem.Read(addr)
	if op&0x80 != 0 {
		bits ^= 0xFF // inverse video
	}
	m.Screen.SetScanlineByte(m.beamX, m.beamY, bits)
	m.beamX++
	m.started = true
	return 0x00 // ULA forces NOP
}

// RunFrame executes approximately one 50 Hz frame, stepping the CPU and driving
// the ZX8x interrupts from a T-state line clock. The maskable INT (once per
// scanline) un-halts the display loop's per-line HALT; when the ZX81 SLOW-mode
// NMI generator is on, an NMI is pulsed once per line to pace the borders.
// Driving these from the step loop (not the M1 hook) means they still fire while
// the CPU is halted, where no opcode is fetched.
func (m *Machine) RunFrame() {
	endT := m.CPU.Tstates() + FrameTStates
	for m.CPU.Tstates() < endT {
		// Border lines are paced by the NMI generator: one NMI per scanline.
		t := m.CPU.Tstates()
		for t-m.baseT >= uint64(m.line+1)*lineTStates {
			m.line++
			if m.nmiOn {
				m.CPU.PendingNMI.Store(true)
			}
		}
		// Maskable /INT follows NOT(R bit 6); request it on the falling edge.
		// R advances during HALT (RefreshDuringHalt), so this fires even while
		// the display loop is halted waiting for the end-of-scanline INT.
		if m.prevR&0x40 != 0 && m.CPU.R&0x40 == 0 {
			m.CPU.IRQPending.Store(true)
		}
		m.prevR = m.CPU.R
		m.CPU.StepInstructionWithIRQ()
	}
}

// Reset restores the machine to its power-on state (the ROM re-initialises RAM
// on boot). The CPU hook configuration is preserved.
func (m *Machine) Reset() {
	m.CPU.Reset()
	m.Screen.Clear()
	m.front = nil
	m.beamX = 0
	m.beamY = 0
	m.started = false
	m.vsync = false
	m.nmiOn = false
	m.baseT = m.CPU.Tstates()
	m.line = 0
	m.prevR = 0
}

// ZX81/ZX80 LOAD-completion re-entry points: the ROM address the SAVE/LOAD
// routine returns to after a successful load. These match the standard ROMs.
const (
	zx81ResumePC = 0x0207
	zx80ResumePC = 0x0283
)

// InjectP loads a ZX81 .P snapshot (a raw dump from $4009, including the saved
// system variables) and resumes execution at the ROM's LOAD-return entry, so
// the restored program continues as if it had just loaded from tape.
func (m *Machine) InjectP(data []byte) {
	LoadP(m.Mem, data)
	m.CPU.PC = zx81ResumePC
	m.CPU.I = 0x1E
}

// InjectO loads a ZX80 .O snapshot (raw dump from $4000) and resumes execution.
func (m *Machine) InjectO(data []byte) {
	LoadO(m.Mem, data)
	m.CPU.PC = zx80ResumePC
	m.CPU.I = 0x0E
}

// Image returns the most recently presented frame (or the in-progress picture
// if none has been presented yet).
func (m *Machine) Image() *image.RGBA {
	if m.front != nil {
		return m.front
	}
	return m.Screen.Image()
}

// --- z80.ULA port interface ---

// ReadPort handles ULA reads. A0 = 0 selects the ULA: it starts vertical sync
// (when the NMI generator is off) and returns the keyboard half-row (active
// low) in bits 0-4, with bit 5 = 1, bit 6 = 1 (PAL/50 Hz), bit 7 = cassette.
func (m *Machine) ReadPort(addr uint16) (byte, bool) {
	if addr&0x01 == 0 {
		if !m.nmiOn {
			m.vsync = true
		}
		return (m.KB.Scan(addr) & 0x1F) | 0x60, true
	}
	return 0xFF, true
}

// WritePort handles ULA writes. On the ZX81, an OUT with A0 = 0 ($FE) enables
// the NMI generator and one with A1 = 0 ($FD) disables it. Any OUT ends vertical
// sync, which presents the completed frame.
func (m *Machine) WritePort(addr uint16, val byte) {
	if m.model == roms.ModelZX81 {
		if addr&0x01 == 0 {
			m.nmiOn = true
		}
		if addr&0x02 == 0 {
			m.nmiOn = false
		}
	}
	m.dropSync()
}

// dropSync ends a VSYNC pulse: it presents the rendered frame and rebases the
// line/beam clocks for the next frame.
func (m *Machine) dropSync() {
	if !m.vsync {
		return
	}
	m.vsync = false
	m.front = m.Screen.Image()
	m.Screen.Clear()
	m.baseT = m.CPU.Tstates()
	m.line = 0
	m.beamX = 0
	m.beamY = 0
	m.started = false
}
