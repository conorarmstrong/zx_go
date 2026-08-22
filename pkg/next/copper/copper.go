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

// ClocksPerHCount is how many copper clocks elapse per horizontal-counter
// tick. hcount advances at the 7 MHz pixel clock (448 columns across a
// 224 T-state line), while the copper is clocked from i_CLK_28
// (zxnext.vhd:3942-3944) — so four copper clocks per hcount.
const ClocksPerHCount = 4

// InstructionsPerScanline is the most copper instructions that can retire
// during one scanline of hcountPerLine columns. A MOVE occupies two copper
// clocks — one to raise copper_dout_s and one to clear it
// (device/copper.vhd:88-108) — so the worst case is half the clock budget.
//
// The copper is clocked from the video domain, so this is independent of the
// CPU speed: a caller must NOT scale it by the Z80 clock.
func InstructionsPerScanline(hcountPerLine int) int {
	return ClocksPerScanline(hcountPerLine) / 2
}

// ClocksPerScanline is the copper's whole clock budget for one scanline
// of hcountPerLine columns. This is the unit Step charges in and the
// unit a per-scanline caller should budget in: a MOVE costs two clocks
// and everything else one, so a budget counted in MOVEs alone let a list
// of NOOPs or unreleased WAITs run without ever drawing it down.
func ClocksPerScanline(hcountPerLine int) int {
	return hcountPerLine * ClocksPerHCount
}

// RegWriter is the contract Copper uses to write NextRegs when
// executing a MOVE. The compositor (or the bus's NextReg
// dispatcher) implements it. Step calls this once per MOVE.
type RegWriter interface {
	WriteReg(reg, val byte)
}

// CopperRAMBytes is the size of the copper instruction memory in bytes.
// The FPGA's nr_copper_addr is 11 bits (zxnext.vhd:1194), addressing
// each byte of the 1024 16-bit instructions.
const CopperRAMBytes = MaxInstructions * 2

// Copper holds the instruction memory + cursor + start mode.
type Copper struct {
	program [MaxInstructions]uint16
	// writePtr is the FPGA's nr_copper_addr: an 11-bit BYTE address into
	// the instruction memory, not an instruction index. Modelling it as a
	// 10-bit word index put every NR$61 address at twice its intended
	// word and left the top 1 KB unreachable.
	writePtr uint16
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
	word := c.writePtr >> 1
	if c.writePtr&1 == 0 {
		// copper_msb_we with nr_copper_write_8 = '1' (zxnext.vhd:3977-3978).
		c.program[word] = (c.program[word] & 0x00FF) | (uint16(b) << 8)
	} else {
		// copper_lsb_we (zxnext.vhd:3998-3999).
		c.program[word] = (c.program[word] & 0xFF00) | uint16(b)
	}
	c.writePtr = (c.writePtr + 1) & (CopperRAMBytes - 1)
}

// SetWritePtrLow / SetWritePtrHighAndMode are the NextReg 0x61 /
// 0x62 writes. 0x61 is the low 8 bits of the 10-bit cursor;
// 0x62 carries the high 2 bits + 2-bit start mode + 4 reserved
// bits.
func (c *Copper) SetWritePtrLow(b byte) {
	// nr_copper_addr(7 downto 0) <= nr_wr_dat (zxnext.vhd:5427).
	c.writePtr = (c.writePtr & 0x700) | uint16(b)
}

// SetWritePtrHighAndMode sets the high 3 cursor bits (val bits 2-0)
// AND the start mode (val bits 7-6), per zxnext.vhd:5430-5431:
//
//	nr_62_copper_mode <= nr_wr_dat(7 downto 6);
//	nr_copper_addr(10 downto 8) <= nr_wr_dat(2 downto 0);
//
// The program counter restarts only when the mode actually CHANGES to
// 01 or 11 (device/copper.vhd:70-76 gates the reset on
// `last_state_s /= copper_en_i`). Restarting on every write meant guest
// code that set the cursor's high bits — which has to go through this
// same register — silently rewound its own running list.
func (c *Copper) SetWritePtrHighAndMode(b byte) {
	c.writePtr = (c.writePtr & 0xFF) | (uint16(b&0x07) << 8)
	newMode := StartMode((b >> 6) & 0x03)
	changed := newMode != c.mode
	c.mode = newMode
	switch newMode {
	case StartStop:
		c.stopped = true
	case StartFromZero, StartOnVBL:
		if changed {
			c.pc = 0
		}
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

// Step advances the copper against the supplied raster position,
// spending at most maxClocks copper clocks. Returns the number of clocks
// actually spent, which is what a caller subtracts from a shared budget.
//
// A MOVE costs two clocks — one to raise copper_dout_s and one to clear
// it (device/copper.vhd:88-108) — and every other outcome costs one:
// a NOOP, a WAIT that releases, and a WAIT that does not. Returning only
// the MOVE count left the other three free, so a list dominated by them
// never drew the caller's budget down; with the end-of-list wrap that
// meant such a list lapped itself many times per scanline.
//
// maxClocks lets callers spread execution across scanlines as the
// hardware does. Pass ClocksPerScanline from a per-scanline render loop.
// Passing a larger number is useful for tests that want to
// "fast-forward" to a stable state.
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
func (c *Copper) Step(scanline uint16, hcount uint16, maxClocks int) int {
	// VBL auto-restart: in StartOnVBL the program counter resets to 0 at
	// the start of each frame, i.e. when the raster wraps back to the top.
	if c.mode == StartOnVBL && scanline < c.lastScanline {
		c.pc = 0
	}
	c.lastScanline = scanline
	if c.stopped {
		return 0
	}
	spent := 0
	for spent < maxClocks && c.pc < MaxInstructions {
		inst := Decode(c.program[c.pc])
		switch inst.Op {
		case OpMOVE:
			if c.regs != nil {
				c.regs.WriteReg(inst.Reg, inst.Val)
			}
			c.pc++
			spent += 2 // dout raised, then cleared
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
				spent++
				continue
			}
			// Not yet — park here. The clock that tested the condition
			// is spent whether or not it released.
			return spent + 1
		case OpHALT:
			c.stopped = true
			return spent + 1
		case OpNOOP:
			c.pc++
			spent++
		}
	}
	// Run off the end and the address counter wraps: copper_list_addr_s
	// is a plain 10-bit counter (device/copper.vhd:48) with no terminal
	// condition, so a list without a never-releasing WAIT runs round
	// again. We used to stop instead, which silently ended any list that
	// relied on the wrap.
	if c.pc >= MaxInstructions {
		c.pc = 0
	}
	return spent
}
