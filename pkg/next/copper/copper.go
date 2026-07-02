// Package copper implements the Spectrum Next's Copper
// coprocessor: 1024 16-bit instructions, MOVE / WAIT / NOOP /
// HALT, four start modes.
//
// Implemented:
//
//   - Instruction storage (2 KB = 1024 × 16-bit)
//   - NextReg 0x60 byte-by-byte data write with auto-increment
//     into a current instruction-memory cursor
//   - NextReg 0x61 / 0x62 control: 10-bit cursor index + 2-bit
//     start mode
//   - Decoded MOVE / WAIT / NOOP / HALT opcodes
//   - Step(scanline, hpos) that walks the program against the
//     supplied raster position, executing MOVE writes through a
//     callback and respecting WAIT
//   - VBL auto-restart (StartOnVBL resets the program counter at
//     the top of each frame)
//
// Not implemented: tight raster-precise execution against per-T-state CPU
// stepping — the execution model uses a scanline+hpos quantum instead, so
// it is functional but not cycle-accurate.
package copper

// Instruction is one decoded copper opcode.
type Instruction struct {
	Op  Op
	Reg byte   // for MOVE: which NextReg to write
	Val byte   // for MOVE: byte to write
	Y   uint16 // for WAIT: target scanline (0..511)
	X   byte   // for WAIT: target hpos / 8
}

// Op identifies the four copper opcodes.
type Op int

const (
	OpNOOP Op = iota
	OpMOVE
	OpWAIT
	OpHALT
)

// StartMode is the 2-bit selector in NextReg 0x62.
type StartMode byte

const (
	StartStop     StartMode = 0 // copper halted
	StartFromZero StartMode = 1 // run from instruction 0 once
	StartContinue StartMode = 2 // continue from current cursor
	StartOnVBL    StartMode = 3 // restart from 0 every VBL
)

// MaxInstructions is the size of the copper instruction memory in
// 16-bit words.
const MaxInstructions = 1024

// RegWriter is the contract Copper uses to write NextRegs when
// executing a MOVE. The compositor (or the bus's NextReg
// dispatcher) implements it. Step calls this once per MOVE.
type RegWriter interface {
	WriteReg(reg, val byte)
}

// Copper holds the instruction memory + cursor + start mode.
type Copper struct {
	program  [MaxInstructions]uint16
	writePtr uint16 // pattern-RAM write head, byte-granular
	hi       byte   // staged high byte for the two-byte write
	hiSet    bool
	mode     StartMode
	pc       uint16 // execution pointer
	stopped  bool
	regs     RegWriter
	// lastScanline tracks the previous Step's raster line so a wrap back
	// to the top of the frame can trigger the StartOnVBL program restart.
	lastScanline uint16
}

// New returns an empty copper.
func New() *Copper { return &Copper{stopped: true} }

// SetRegWriter installs the NextReg writer. Required for MOVE
// instructions to take effect; otherwise they are silent no-ops.
func (c *Copper) SetRegWriter(rw RegWriter) { c.regs = rw }

// WriteData stores one byte into the copper instruction memory at
// the current write cursor. Copper instructions are 16 bits wide
// and arrive over NextReg 0x60 in two consecutive byte writes
// (high byte first); WriteData latches the first byte, then
// commits the pair on the second.
func (c *Copper) WriteData(b byte) {
	if !c.hiSet {
		c.hi = b
		c.hiSet = true
		return
	}
	c.hiSet = false
	if c.writePtr < MaxInstructions {
		c.program[c.writePtr] = (uint16(c.hi) << 8) | uint16(b)
	}
	c.writePtr++
	if c.writePtr >= MaxInstructions {
		c.writePtr = 0
	}
}

// SetWritePtrLow / SetWritePtrHighAndMode are the NextReg 0x61 /
// 0x62 writes. 0x61 is the low 8 bits of the 10-bit cursor;
// 0x62 carries the high 2 bits + 2-bit start mode + 4 reserved
// bits.
func (c *Copper) SetWritePtrLow(b byte) {
	c.writePtr = (c.writePtr & 0x300) | uint16(b)
	// Setting the write cursor starts a fresh 16-bit instruction write, so the
	// two-byte (hi/lo) pairing phase resets. Without this a stray odd NR$60
	// byte left staged before the cursor move — e.g. the dispatcher reset
	// writing NR$60=$00 — pairs off-by-one with the following program stream,
	// turning a real "WAIT y; MOVE r,v" list into garbage "MOVE NR$01..,$16"
	// MOVEs that clobber the whole NextReg config (Nextoid reset-to-Welcome bug).
	c.hiSet = false
}

// SetWritePtrHighAndMode sets the high 2 cursor bits (val bits 0-1)
// AND the start-mode (val bits 6-7).
func (c *Copper) SetWritePtrHighAndMode(b byte) {
	c.writePtr = (c.writePtr & 0xFF) | (uint16(b&0x03) << 8)
	c.hiSet = false // cursor move resets the two-byte write phase (see SetWritePtrLow)
	c.mode = StartMode((b >> 6) & 0x03)
	switch c.mode {
	case StartStop:
		c.stopped = true
	case StartFromZero, StartOnVBL:
		c.pc = 0
		c.stopped = false
	case StartContinue:
		c.stopped = false
	}
}

// Cursor returns the current write cursor (for debugging / tests).
func (c *Copper) Cursor() uint16 { return c.writePtr }

// Mode returns the current start mode.
func (c *Copper) Mode() StartMode { return c.mode }

// Instruction returns the decoded instruction at index i. Indexes
// past MaxInstructions return a NOOP.
func (c *Copper) Instruction(i uint16) Instruction {
	if i >= MaxInstructions {
		return Instruction{Op: OpNOOP}
	}
	return Decode(c.program[i])
}

// Decode parses a raw 16-bit instruction word into Instruction.
//
// Per the FPGA encoding (device/copper.vhd):
//   - MOVE: bit 15 = 0; bits 14-8 = NextReg index (7 bits);
//     bits 7-0 = value byte.
//   - WAIT: bit 15 = 1; bits 14-9 = horizontal position (0..63, in
//     8-pixel columns); bits 8-0 = vertical scanline (0..511).
//   - HALT: special-case WAIT 0xFFFF (waits for line 511, hpos 63
//     — never reached).
//   - NOOP: a MOVE whose NextReg index (bits 14-8) is zero. The
//     hardware suppresses the write pulse purely on the register
//     field being zero (copper.vhd:104 — "MOVE 0,0" / NOP test is
//     on copper_list_data_i(14 downto 8), the value byte is NOT
//     considered), so MOVE reg 0 with any value is a NOOP.
func Decode(w uint16) Instruction {
	if w == 0xFFFF {
		return Instruction{Op: OpHALT}
	}
	if w&0x8000 == 0 {
		reg := byte((w >> 8) & 0x7F)
		val := byte(w & 0xFF)
		if reg == 0 {
			return Instruction{Op: OpNOOP}
		}
		return Instruction{Op: OpMOVE, Reg: reg, Val: val}
	}
	// WAIT
	x := byte((w >> 9) & 0x3F)
	y := w & 0x01FF
	return Instruction{Op: OpWAIT, X: x, Y: y}
}

// WaitHThreshold is the horizontal raster counter (hcount, 0..511 in
// pixels) at or above which a WAIT with column field x releases on its
// target scanline. The hardware compares hcount_i >= (x << 3) + 12, i.e.
// the 6-bit column is taken as 8-pixel units with a fixed +12 pixel offset
// (device/copper.vhd:94).
func WaitHThreshold(x byte) uint16 { return uint16(x)<<3 + 12 }

// Step advances the copper by at most maxInstr instructions
// against the supplied raster position. Returns the number of
// MOVE instructions actually executed. MOVE writes go through
// the RegWriter; WAITs that haven't been satisfied leave pc
// parked and stop the step. HALT stops the copper.
//
// maxInstr lets callers spread instruction execution across
// scanlines as real hardware does (one Copper cycle per CPU
// cycle, roughly). Pass 1 from a per-scanline render loop and
// the copper executes at most one instruction per call. Passing
// a larger number is useful for tests that want to "fast-forward"
// to a stable state.
//
// Re-entry guard: if a MOVE writes to NextRegs that mutate the
// Copper's own state (0x60-0x62), the writes are buffered through
// the dispatcher and applied as usual; Step doesn't reload its own
// pc / writePtr fields mid-loop, so a re-entrant mutation only takes
// effect on the NEXT Step call.
//
// scanline is the raster line (vcount, 0..511). hcount is the raster
// horizontal counter (0..511 in pixels) — the same units the FPGA's
// hcount_i carries; a WAIT releases at hcount >= (x<<3)+12 on its target
// line (see WaitHThreshold). A per-scanline caller passes the end-of-line
// hcount (>= 511) so every WAIT on the line releases on that line.
func (c *Copper) Step(scanline uint16, hcount uint16, maxInstr int) int {
	// VBL auto-restart: in StartOnVBL the program counter resets to 0 at
	// the start of each frame, i.e. when the raster wraps back to the top.
	if c.mode == StartOnVBL && scanline < c.lastScanline {
		c.pc = 0
	}
	c.lastScanline = scanline
	if c.stopped {
		return 0
	}
	executed := 0
	for n := 0; n < maxInstr && c.pc < MaxInstructions; n++ {
		inst := Decode(c.program[c.pc])
		switch inst.Op {
		case OpMOVE:
			if c.regs != nil {
				c.regs.WriteReg(inst.Reg, inst.Val)
			}
			c.pc++
			executed++
		case OpWAIT:
			// Release when the raster reaches the target line and the
			// horizontal threshold: hcount >= (X<<3)+12 on line Y
			// (device/copper.vhd:94). The scanline > Y branch is the
			// functional-model fallback for a per-scanline caller that has
			// already advanced past the target line (the hardware, clocked
			// every pixel, releases exactly on line Y; a caller stepping each
			// line hits Y exactly, so this only matters if a line is skipped).
			if scanline > inst.Y ||
				(scanline == inst.Y && hcount >= WaitHThreshold(inst.X)) {
				c.pc++
				continue
			}
			// Not yet — park here.
			return executed
		case OpHALT:
			c.stopped = true
			return executed
		case OpNOOP:
			c.pc++
		}
	}
	// Did we run off the end?
	if c.pc >= MaxInstructions {
		if c.mode == StartOnVBL {
			// Park at the end of the list; the program counter is reset
			// to 0 at the start of the next frame by the VBL check at
			// Step entry (so the list restarts precisely on the raster
			// wrap, not when it happens to run off the end).
			c.pc = MaxInstructions
		} else {
			c.stopped = true
		}
	}
	return executed
}
