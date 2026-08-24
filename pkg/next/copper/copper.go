// Package copper implements the Spectrum Next's Copper
// coprocessor: 1024 16-bit instructions, MOVE / WAIT / NOOP,
// four start modes.
//
// There is no halt. A list is ended by parking it on a WAIT its raster never
// satisfies, idiomatically $FFFF, which is simply WAIT x=63, y=511 and parks
// because vcount never reaches 511. device/copper.vhd:91-98 has one WAIT
// branch and no other stop condition, so a list in mode 11 runs again at every
// frame start however it was terminated.
//
// Implemented:
//
//   - Instruction storage (2 KB = 1024 × 16-bit)
//   - NextReg 0x60 byte-by-byte data write with auto-increment
//     into a current instruction-memory cursor
//   - NextReg 0x61 / 0x62 control: 10-bit cursor index + 2-bit
//     start mode
//   - Decoded MOVE / WAIT / NOOP opcodes
//   - Step(scanline, hcount, maxClocks) that walks the program against the
//     supplied raster position, executing MOVE writes through a
//     callback and respecting WAIT
//   - VBL auto-restart (StartOnVBL resets the program counter at
//     the top of each frame)
//
// Timing. The copper is clocked from i_CLK_28 while its hcount ticks at
// i_CLK_7 (zxnext.vhd:43,46,3944), so a raster column is four copper clocks:
// see ClocksPerHCount. A MOVE costs two of them (device/copper.vhd:87,105) and
// everything else one, which Step charges for and returns, so a caller stepping
// column by column reproduces the hardware's real throughput of one instruction
// per quarter-pixel and one MOVE per half-pixel. hcount is hc_ula, whose origin
// sits twelve columns before displayed pixel 0: see HCountOrigin.
//
// What that leaves. A quarter-pixel is below what a 256-pixel raster can
// represent, so Step is charged per column rather than per clock and a MOVE's
// effect is reported against the pixel its column generates (see Wrote). The
// hardware then takes a further two or three 28 MHz clocks to push the write
// through the NextReg arbiter (zxnext.vhd:4709,4729,4775), which is again under
// one pixel and is not modelled.
package copper

// Instruction is one decoded copper opcode.
type Instruction struct {
	Op  Op
	Reg byte   // for MOVE: which NextReg to write
	Val byte   // for MOVE: byte to write
	Y   uint16 // for WAIT: target scanline (0..511)
	X   byte   // for WAIT: target hpos / 8
}

// Op identifies the three copper opcodes.
type Op int

const (
	OpNOOP Op = iota
	OpMOVE
	OpWAIT
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
// of hcountPerLine columns: a MOVE costs two clocks and everything else one,
// so a budget counted in MOVEs alone let a list of NOOPs or unreleased WAITs
// run without ever drawing it down.
//
// It is the line's total, not a quantum to hand Step. A render loop pays the
// copper per column, ClocksPerHCount at a time, because a line's worth of
// clocks given at once lets a burst of MOVEs retire before the first pixel is
// generated when on hardware it occupies half a pixel each.
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
	// lastMode is the FPGA's last_state_s (copper.vhd:50): the mode as of the
	// previous clock. The device compares it against copper_en_i on every clock
	// and acts on the difference, so a mode change is noticed by the running
	// engine rather than applied by whatever wrote it. That distinction matters
	// because a MOVE can write NextReg $62 (pkg/next/wire.go routes it to
	// SetWritePtrHighAndMode), so a list can change its own mode from inside its
	// own execution.
	lastMode StartMode
	pc       uint16 // execution pointer
	stopped  bool
	regs     RegWriter
	// lastScanline tracks the previous Step's raster line so a wrap back
	// to the top of the frame can trigger the StartOnVBL program restart.
	lastScanline uint16
	// executing is raised only across a MOVE's register write. It carries
	// no hardware meaning: it exists so a debugger watching a NextReg can
	// tell a copper MOVE from a CPU OUT, since both arrive at the same
	// Dispatcher.WriteReg and a MOVE reported against the Z80's PC would
	// name an instruction that had nothing to do with the write.
	executing bool
	// wrote records whether the LAST Step raised the MOVE write pulse. It is
	// the model's copper_dout_s, sampled per call instead of per clock: a
	// render loop stepping the copper column by column uses it to split the
	// row's composition at the pixel a MOVE actually wrote in, rather than
	// assuming the write landed at a segment boundary.
	wrote bool
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
// code that set the cursor's high bits, which has to go through this
// same register, silently rewound its own running list.
//
// This records the new mode and nothing else. The restart is the engine's to
// perform, on the clock it notices last_state_s differ, because that is where
// the device does it: the comparison is the first arm of the per-clock chain
// and it consumes the clock, so no instruction runs on it. Resetting pc here
// instead put the reset in the middle of whatever instruction was executing,
// and a MOVE to $62 then had its own pc++ applied on top of the reset.
func (c *Copper) SetWritePtrHighAndMode(b byte) {
	c.writePtr = (c.writePtr & 0xFF) | (uint16(b&0x07) << 8)
	c.mode = StartMode((b >> 6) & 0x03)
	c.stopped = c.mode == StartStop
}

// Idle reports that clocking this Copper cannot change anything: it is stopped
// and the engine has already latched that mode, so every clock would fall
// through the chain's final else (copper.vhd:112-114) and do nothing.
//
// It exists so a render loop can skip a line's worth of clocks rather than pay
// for them one column at a time. It is deliberately false while a mode change
// is pending, because that change is the engine's to notice and the clock that
// notices it is the one that restarts the list.
func (c *Copper) Idle() bool { return c.stopped && c.lastMode == c.mode }

// Cursor returns the current write cursor (for debugging / tests).
func (c *Copper) Cursor() uint16 { return c.writePtr }

// Mode returns the current start mode.
func (c *Copper) Mode() StartMode { return c.mode }

// PC returns the execution pointer: the index of the instruction the copper
// is on. During a MOVE's register write this is the MOVE itself, because the
// increment happens after the write.
func (c *Copper) PC() uint16 { return c.pc }

// Executing reports whether the copper is inside a MOVE's register write.
// A NextReg trace callback that sees this true was called by the copper; one
// that sees it false was called by the CPU (or by a reset, or by wiring).
func (c *Copper) Executing() bool { return c.executing }

// Wrote reports whether the most recent Step raised a MOVE's NextReg write
// pulse. It describes that one call, not the copper's history, so a caller
// stepping per raster column learns which column the write landed in.
//
// A MOVE whose 7-bit register field is zero does not count: the hardware
// suppresses the pulse on that field alone (device/copper.vhd:104), which
// Decode already reports as OpNOOP.
func (c *Copper) Wrote() bool { return c.wrote }

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
//     $FFFF is not a separate opcode: it is WAIT x=63, y=511, the
//     idiomatic list terminator, and it parks on the line test alone.
//   - NOOP: a MOVE whose NextReg index (bits 14-8) is zero. The
//     hardware suppresses the write pulse purely on the register
//     field being zero (copper.vhd:104 — "MOVE 0,0" / NOP test is
//     on copper_list_data_i(14 downto 8), the value byte is NOT
//     considered), so MOVE reg 0 with any value is a NOOP.
func Decode(w uint16) Instruction {
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

// HCountOrigin is the hcount at which the display generates its first pixel.
// The copper's hcount_i is hc_ula (zxnext.vhd:3949, fed from o_hc_ula at
// zxnext.vhd:6737), which is reset at c_min_hactive - 12
// (video/zxula_timing.vhd:423-424), so its zero point is twelve columns before
// the 256-pixel active area. video/zxula.vhd:44-46 says it plainly: the
// practical counter's pixel 0 "corresponds to ULA count i_hc = 0xC".
//
// It is not decoration. Handing Step a raw display x treats hcount 0 as pixel
// 0 and so releases every WAIT twelve pixels late.
const HCountOrigin = 12

// HCountForPixel maps a displayed pixel (0..255 across the 256-pixel active
// area) to the hcount the copper sees while that pixel is being generated.
// Callers driving Step from a render loop must pass this, not the raw x.
func HCountForPixel(p int) uint16 { return uint16(p + HCountOrigin) }

// WaitHThreshold is the horizontal raster counter (hcount) at or above which a
// WAIT with column field x releases on its target scanline. The hardware
// compares hcount_i >= (x << 3) + 12 (device/copper.vhd:94): the 6-bit column
// is taken as 8-pixel units, and the +12 is exactly HCountOrigin, so a WAIT for
// column X releases at displayed pixel 8X, for every column but the last.
//
// The add is NINE bits wide and WRAPS. copper.vhd:94 writes
// `unsigned(copper_list_data_i(14 downto 9)&"000") + 12`, whose left operand is
// the 6-bit column concatenated with three zeros, and numeric_std's "+" returns
// a result the width of its left operand. Column 63 therefore gives
// 63*8+12 = 516, which truncates to 4, and that WAIT releases four columns into
// the line rather than never. Columns 0..62 top out at 508 and do not wrap.
// Confirmed under GHDL against the real device/copper.vhd
// (_tools/copper-vhdl-test, "wait-col63-wraps" against its "wait-col62-parks"
// control).
//
// Thresholds still land past the end of a line for a caller whose line is
// shorter than 512 columns: a full line is 448 (48K) or 456 (128K) columns
// (video/zxula_timing.vhd:160,196), and those WAITs do not release, which is
// the hardware's behaviour and not a modelling limit.
func WaitHThreshold(x byte) uint16 { return HCountForPixel(int(x)*8) & 0x1FF }

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
// maxClocks lets callers spread execution across the raster as the hardware
// does. A render loop walking the line column by column passes ClocksPerHCount,
// less anything a MOVE begun in the previous column already spent past its end:
// spent can exceed maxClocks by one clock, because an instruction that has
// started runs to completion, and the caller carries that as a debt against the
// next column rather than letting it gain a quarter-pixel on every WAIT
// release. Passing a larger number is useful for tests that want to
// "fast-forward" to a stable state.
//
// Re-entry guard: if a MOVE writes to NextRegs that mutate the
// Copper's own state (0x60-0x62), the writes are buffered through
// the dispatcher and applied as usual; Step doesn't reload its own
// pc / writePtr fields mid-loop, so a re-entrant mutation only takes
// effect on the NEXT Step call.
//
// scanline is the raster line (vcount). hcount is the raster horizontal
// counter in the same units the FPGA's hcount_i carries, which is hc_ula: a
// render loop converts a displayed pixel to it with HCountForPixel, and a WAIT
// releases at hcount >= (x<<3)+12 on its target line (see WaitHThreshold).
// A caller that walks only part of the line leaves any WAIT beyond its last
// column parked, which is right for the columns past the end of the line and
// wrong for the ones it skipped.
func (c *Copper) Step(scanline uint16, hcount uint16, maxClocks int) int {
	// Wrote describes this call and no other, so clear it before anything can
	// return early.
	c.wrote = false
	// VBL auto-restart: in StartOnVBL the program counter resets to 0 at
	// the start of each frame, i.e. when the raster wraps back to the top.
	if c.mode == StartOnVBL && scanline < c.lastScanline {
		c.pc = 0
	}
	c.lastScanline = scanline
	spent := 0
	for spent < maxClocks && c.pc < MaxInstructions {
		// First arm of the device's per-clock chain (copper.vhd:70-76): the
		// mode has changed since the previous clock. Latch it, restart the
		// address for the two modes that restart, and fall through to the end
		// of the clock. Nothing executes on it.
		if c.lastMode != c.mode {
			c.lastMode = c.mode
			if c.mode == StartFromZero || c.mode == StartOnVBL {
				c.pc = 0
			}
			spent++
			continue
		}
		// Final else of the same chain (copper.vhd:112-114): mode 00 executes
		// nothing. Tested every clock, so a MOVE that stops the Copper stops it
		// where it stands rather than at the end of the caller's budget.
		if c.stopped {
			return spent
		}
		inst := Decode(c.program[c.pc])
		switch inst.Op {
		case OpMOVE:
			// copper_dout_s rises for any MOVE with a non-zero register field
			// (device/copper.vhd:104), whether or not anything is wired to
			// receive it, so this does not depend on regs.
			c.wrote = true
			if c.regs != nil {
				// Raised across the write only, and cleared before the pc
				// moves on, so a trace callback reads the MOVE's own index.
				c.executing = true
				c.regs.WriteReg(inst.Reg, inst.Val)
				c.executing = false
			}
			c.pc++
			spent += 2 // dout raised, then cleared
		case OpWAIT:
			// Release when the raster is ON the target line and at or past the
			// horizontal threshold (device/copper.vhd:94). The vertical test is
			// an EQUALITY there (`vcount_i = unsigned(...)`), so a WAIT whose
			// line is already behind the raster parks until that line comes
			// round again next frame; it does not release late. A "scanline > Y"
			// fallback used to cover a caller that skipped lines, but the render
			// loop presents every line and every column, so all it did was run
			// a list's later WAITs a frame early.
			if scanline == inst.Y && hcount >= WaitHThreshold(inst.X) {
				c.pc++
				spent++
				continue
			}
			// Not yet — park here. The clock that tested the condition
			// is spent whether or not it released.
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
