package copper

import "testing"

type fakeRegWriter struct {
	writes []struct{ reg, val byte }
}

func (f *fakeRegWriter) WriteReg(reg, val byte) {
	f.writes = append(f.writes, struct{ reg, val byte }{reg, val})
}

func TestDecodeMOVE(t *testing.T) {
	w := uint16(0x4255) // bit 15 = 0; reg 0x42; val 0x55
	got := Decode(w)
	if got.Op != OpMOVE || got.Reg != 0x42 || got.Val != 0x55 {
		t.Errorf("Decode(0x4255) = %+v, want MOVE reg=0x42 val=0x55", got)
	}
}

func TestDecodeWAIT(t *testing.T) {
	// bit 15 = 1; x = 4 (in bits 14-9); y = 100 (in bits 8-0)
	w := uint16(0x8000) | (uint16(4) << 9) | 100
	got := Decode(w)
	if got.Op != OpWAIT || got.X != 4 || got.Y != 100 {
		t.Errorf("Decode WAIT 4/100 = %+v", got)
	}
}

func TestDecodeNOOP(t *testing.T) {
	if Decode(0x0000).Op != OpNOOP {
		t.Errorf("0x0000 should decode as NOOP")
	}
}

func TestWriteDataFillsBothHalvesOfAWord(t *testing.T) {
	c := New()
	// The cursor is a byte address, so word 0x10 starts at byte 0x20.
	c.SetWritePtrLow(0x20)
	c.WriteData(0x42) // high half
	c.WriteData(0x55) // low half
	if got := c.Instruction(0x10); got.Op != OpMOVE || got.Reg != 0x42 || got.Val != 0x55 {
		t.Errorf("instruction[0x10] = %+v, want MOVE 0x42/0x55", got)
	}
}

func TestStartFromZeroRunsProgram(t *testing.T) {
	c := New()
	// MOVE reg 0x07, val 0x02 at index 0.
	c.SetWritePtrLow(0)
	c.WriteData(0x07)
	c.WriteData(0x02)
	// The $FFFF list terminator at index 1: WAIT x=63, y=511, which parks
	// because line 511 never arrives.
	c.WriteData(0xFF)
	c.WriteData(0xFF)

	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	// Start from zero (mode 1).
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)
	// Three clocks: the MOVE costs two, the WAIT test one.
	c.Step(0, 0, 3)

	if len(rw.writes) != 1 || rw.writes[0].reg != 0x07 || rw.writes[0].val != 0x02 {
		t.Errorf("MOVE not executed: writes = %+v", rw.writes)
	}
	if c.PC() != 1 {
		t.Errorf("pc = %d after the MOVE, want 1 (parked on the terminator)", c.PC())
	}
	// Parked, not stopped: only NR$62 mode 00 stops the copper
	// (device/copper.vhd:112-115).
	if c.stopped {
		t.Errorf("the $FFFF terminator must park the copper, not stop it")
	}
}

func TestWaitParksUntilRasterReached(t *testing.T) {
	c := New()
	// WAIT y=100, x=0 at index 0.
	wait := uint16(0x8000) | 100
	c.SetWritePtrLow(0)
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait))
	// MOVE 0x07, 0x03 at index 1.
	c.WriteData(0x07)
	c.WriteData(0x03)

	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// Step at scanline 50, end-of-line hcount — not yet past WAIT 100.
	c.Step(50, 511, 4)
	if len(rw.writes) != 0 {
		t.Errorf("MOVE fired before WAIT was satisfied: writes = %+v", rw.writes)
	}

	// Step at scanline 100, end-of-line hcount — WAIT released, MOVE executes.
	c.Step(100, 511, 4)
	if len(rw.writes) != 1 || rw.writes[0].reg != 0x07 || rw.writes[0].val != 0x03 {
		t.Errorf("MOVE not executed after WAIT released: writes = %+v", rw.writes)
	}
}

func TestStartStopDoesntRun(t *testing.T) {
	c := New()
	c.WriteData(0x07)
	c.WriteData(0x02)
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartStop) << 6)
	c.Step(0, 0, 4)
	if len(rw.writes) != 0 {
		t.Errorf("StartStop should keep copper halted; writes = %+v", rw.writes)
	}
}

// TestStepMaxInstrLimitsExecution pins the per-call instruction
// budget: with three MOVEs in the program, Step(_,_,1) executes
// exactly one of them.
func TestStepMaxInstrLimitsExecution(t *testing.T) {
	c := New()
	// Three MOVEs at indexes 0..2.
	for i := 0; i < 3; i++ {
		c.WriteData(0x07)
		c.WriteData(byte(0x10 + i))
	}
	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// Enabling the Copper costs a clock. last_state_s now differs from
	// copper_en_i, and that comparison is the first arm of the device's
	// per-clock chain (copper.vhd:70-76): it latches the mode, resets the
	// address, and falls through to the end of the clock without executing.
	c.Step(0, 0, 1)
	if len(rw.writes) != 0 {
		t.Fatalf("the enabling clock executed %d MOVEs, want 0: it is spent on the "+
			"restart, not on an instruction", len(rw.writes))
	}

	// Then one MOVE per clock.
	c.Step(0, 0, 1)
	if len(rw.writes) != 1 || rw.writes[0].val != 0x10 {
		t.Errorf("first executing clock ran %d MOVEs, want exactly 1; first val = %#x, want 0x10",
			len(rw.writes), rw.writes[0].val)
	}
	c.Step(0, 0, 1)
	if len(rw.writes) != 2 || rw.writes[1].val != 0x11 {
		t.Errorf("second executing clock: expected 2 MOVEs total; got %+v", rw.writes)
	}
}

// TestMOVEIntoCopperOwnRegistersDoesNotCrash exercises the
// re-entrant case where a MOVE instruction targets the Copper's
// own data / control registers. In production, the shared NextReg
// dispatcher is the RegWriter, so a MOVE to 0x60
// re-enters the Copper's WriteData via the dispatcher's OnWrite
// callback. The guard is that Step doesn't reload pc / mode
// fields mid-loop; this test pins that nothing crashes and the
// outer Step's pc advances normally.
type reentrantWriter struct {
	c *Copper
}

func (r *reentrantWriter) WriteReg(reg, val byte) {
	// Simulate dispatcher → Copper handler re-entry for 0x60.
	if reg == 0x60 {
		r.c.WriteData(val)
	}
}

func TestMOVEIntoCopperOwnRegistersDoesNotCrash(t *testing.T) {
	c := New()
	// Program at index 0: MOVE 0x60, 0xAB (re-enters); then the $FFFF
	// terminator, which parks the list.
	c.WriteData(0x60)
	c.WriteData(0xAB)
	c.WriteData(0xFF)
	c.WriteData(0xFF)

	c.SetRegWriter(&reentrantWriter{c: c})
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	// Step with budget 4. MOVE fires, calls reentrantWriter
	// which calls c.WriteData (mutating writePtr) — does NOT
	// disturb the outer Step's pc.
	c.Step(0, 0, 4)
	if c.PC() != 1 {
		t.Errorf("pc = %d, want 1: the MOVE ran and the list parked on the terminator", c.PC())
	}
}

func TestCursorWraps(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x07) // high 3 bits set
	c.SetWritePtrLow(0xFE)         // -> byte address 0x7FE, the last word
	if c.Cursor() != 0x7FE {
		t.Errorf("Cursor after high-write = %#x, want 0x7FE", c.Cursor())
	}
	// Write past the end — the byte address wraps.
	c.WriteData(0x00) // 0x7FE -> 0x7FF
	c.WriteData(0x00) // 0x7FF -> 0x800, wraps to 0
	if c.Cursor() != 0 {
		t.Errorf("Cursor after wrap = %#x, want 0", c.Cursor())
	}
}

// TestDecodeMOVE_RegMaskedTo7Bits verifies the 7-bit NextReg index
// in MOVE (bits 14:8) is masked to 0..127 — bit 15 distinguishes
// MOVE vs WAIT, so it can never appear in Reg.
func TestDecodeMOVE_RegMaskedTo7Bits(t *testing.T) {
	w := uint16(0x7F55) // bit 15=0, bits 14:8=0x7F, value=0x55
	got := Decode(w)
	if got.Op != OpMOVE || got.Reg != 0x7F {
		t.Errorf("Decode($7F55) = %+v, want MOVE reg=$7F val=$55", got)
	}
}

// TestDecodeWAIT_AllZeroXY is a corner case: a WAIT (0,0) at boot
// would never satisfy because the copper starts past the position.
func TestDecodeWAIT_AllZeroXY(t *testing.T) {
	w := uint16(0x8000) // bit 15 = 1, x=0, y=0
	got := Decode(w)
	if got.Op != OpWAIT || got.X != 0 || got.Y != 0 {
		t.Errorf("Decode($8000) = %+v, want WAIT 0/0", got)
	}
}

// TestDecodeWAIT_MaxX verifies bits 14:9 (6 bits) decode 0..63.
func TestDecodeWAIT_MaxX(t *testing.T) {
	w := uint16(0x8000) | (uint16(63) << 9) | 510
	got := Decode(w)
	if got.Op != OpWAIT || got.X != 63 || got.Y != 510 {
		t.Errorf("Decode max-x = %+v, want WAIT 63/510", got)
	}
}

// TestSetWritePtrLow_KeepsHighBits verifies that writing the low
// byte preserves the high 2 cursor bits set via NR$62.
func TestSetWritePtrLow_KeepsHighBits(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x02) // bits 0-1 = high cursor, value 2 → high=$02 = 2
	if c.Cursor() != 0x200 {
		t.Fatalf("after HighAndMode($02): cursor = %#x, want $200", c.Cursor())
	}
	c.SetWritePtrLow(0x55)
	if c.Cursor() != 0x255 {
		t.Errorf("after Low($55): cursor = %#x, want $255 (high bits preserved)",
			c.Cursor())
	}
}

// TestMode_MaskedToBits76 verifies SetWritePtrHighAndMode extracts
// mode from bits 7:6 only (bits 5:2 are reserved per docs).
func TestMode_MaskedToBits76(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x7F) // bits 7:6 = 01 (StartFromZero), all other bits set
	if c.Mode() != StartFromZero {
		t.Errorf("Mode after $7F: %v, want StartFromZero", c.Mode())
	}
	c.SetWritePtrHighAndMode(0xBF) // bits 7:6 = 10 (StartContinue)
	if c.Mode() != StartContinue {
		t.Errorf("Mode after $BF: %v, want StartContinue", c.Mode())
	}
}

// TestStartStop_HaltsCopper verifies StartStop mode (bits 7:6 = 00)
// halts the copper.
func TestStartStop_HaltsCopper(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(0x40) // bits 7:6 = 01, start from zero
	// Now stop.
	c.SetWritePtrHighAndMode(0x00) // bits 7:6 = 00, stop
	if c.Mode() != StartStop {
		t.Errorf("after StartStop write: Mode = %v, want StartStop", c.Mode())
	}
}

// TestNewCopper_StartsStopped — fresh Copper instance has stopped=true.
func TestNewCopper_StartsStopped(t *testing.T) {
	c := New()
	if c.Mode() != StartStop {
		t.Errorf("New Copper Mode = %v, want StartStop", c.Mode())
	}
}

// TestCursorSetResetsBytePhase locks in that setting the NR$61/$62 write cursor
// resets the NR$60 hi/lo byte-pairing phase. A stray odd byte staged before the
// cursor move (e.g. the dispatcher reset writing NR$60=$00) must NOT pair
// off-by-one with the following program stream — that turned NextZXOS's real
// copper list into garbage "MOVE NR$01..,$16" writes that clobbered the whole
// NextReg config and reset Nextoid to the Welcome screen.
func TestCursorSetResetsBytePhase(t *testing.T) {
	for _, via62 := range []bool{false, true} {
		c := New()
		c.WriteData(0x00) // stray staged hi byte (the reset write)
		if via62 {
			c.SetWritePtrHighAndMode(0x00) // cursor high + mode, must reset phase
			c.SetWritePtrLow(0x00)
		} else {
			c.SetWritePtrLow(0x00) // cursor low, must reset phase
		}
		// Program: WAIT line 0 (0x8000) then MOVE NR$16,$00 (0x1600).
		c.WriteData(0x80)
		c.WriteData(0x00)
		c.WriteData(0x16)
		c.WriteData(0x00)
		if got := c.Instruction(0); got.Op != OpWAIT || got.Y != 0 {
			t.Errorf("via62=%v: instruction[0]=%+v, want WAIT y=0 (cursor set must reset byte phase)", via62, got)
		}
		if got := c.Instruction(1); got.Op != OpMOVE || got.Reg != 0x16 || got.Val != 0x00 {
			t.Errorf("via62=%v: instruction[1]=%+v, want MOVE NR$16,$00", via62, got)
		}
	}
}

// A mode change is the FIRST arm of the FPGA's per-clock if/elsif chain
// (copper.vhd:70-76): when last_state_s differs from copper_en_i it latches the
// new mode, resets copper_list_addr_s to 0 for modes 01 and 11, and falls
// through to the end of the clock. No instruction executes on that clock. So a
// list that restarts itself resumes AT instruction 0, and the next instruction
// fetched is the one at address 0.
func TestAMoveThatRestartsTheListResumesAtInstructionZero(t *testing.T) {
	c := New()
	// 0: MOVE $40,$01   a marker write so we can see instruction 0 run
	// 1: MOVE $62,<StartFromZero>  restart the list
	// 2: MOVE $41,$02   must never run: the restart sends us back to 0
	c.WriteData(0x40)
	c.WriteData(0x01)
	c.WriteData(0x62)
	c.WriteData(byte(StartFromZero) << 6)
	c.WriteData(0x41)
	c.WriteData(0x02)

	var seen []byte
	c.SetRegWriter(&multiWriter{c: c, seen: &seen})
	// Run in Continue, so the MOVE to $62 is a real mode CHANGE.
	c.SetWritePtrHighAndMode(byte(StartContinue) << 6)
	c.Step(0, 0, 1) // the enabling clock
	c.pc = 0

	// Four clocks: a MOVE costs two (dout raised, then cleared), so this is
	// instruction 0's MOVE followed by instruction 1's, which sets the mode to
	// 01 from inside the list.
	c.Step(0, 0, 4)
	if c.PC() != 2 {
		t.Fatalf("pc = %d after the two MOVEs, want 2: the fixture is not set up "+
			"as intended (writes seen: %v)", c.PC(), seen)
	}

	// One more clock is the restart. It executes nothing and leaves pc at 0,
	// so the next instruction fetched is instruction 0 and instruction 2 is
	// never reached.
	c.Step(0, 0, 1)
	if c.PC() != 0 {
		t.Errorf("pc after a self-restart = %d, want 0: the mode change resets "+
			"copper_list_addr_s and consumes the clock, so nothing advances past it", c.PC())
	}
	for _, reg := range seen {
		if reg == 0x41 {
			t.Errorf("instruction 2 ran after the list restarted itself; writes seen: %v", seen)
		}
	}
}

// copper_en_i = "00" falls to the chain's final else, which executes nothing
// (copper.vhd:112-114). It is evaluated on every clock, not once before the
// list starts, so a MOVE that stops the Copper stops it there and then.
func TestAMoveThatStopsTheCopperStopsItImmediately(t *testing.T) {
	c := New()
	// 0: MOVE $40,$01
	// 1: MOVE $62,<StartStop>
	// 2: MOVE $41,$02   must never run
	c.WriteData(0x40)
	c.WriteData(0x01)
	c.WriteData(0x62)
	c.WriteData(byte(StartStop) << 6)
	c.WriteData(0x41)
	c.WriteData(0x02)

	var seen []byte
	c.SetRegWriter(&multiWriter{c: c, seen: &seen})
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	c.Step(0, 0, 8)

	for _, reg := range seen {
		if reg == 0x41 {
			t.Errorf("the Copper kept executing after a MOVE stopped it; writes seen: %v", seen)
		}
	}
}

// multiWriter records every register a MOVE writes and forwards $62 to the
// Copper, which is the dispatcher path a MOVE to NextReg $62 really takes:
// pkg/next/wire.go routes $62 to SetWritePtrHighAndMode, so a copper list can
// change its own start mode from inside its own execution. A test can then see
// both what ran and what the list did to itself.
type multiWriter struct {
	c    *Copper
	seen *[]byte
}

func (w *multiWriter) WriteReg(reg, val byte) {
	*w.seen = append(*w.seen, reg)
	if reg == 0x62 {
		w.c.SetWritePtrHighAndMode(val)
	}
}

// Idle is the cheap "can this device do anything at all right now" question a
// render loop asks before paying for a line's worth of clocks. It has to be
// false the moment a mode change is pending, or enabling the Copper would not
// take effect until something else woke the loop up.
func TestIdleIsFalseWhileAModeChangeIsPending(t *testing.T) {
	c := New()
	if !c.Idle() {
		t.Fatal("a fresh Copper is stopped and has nothing pending, so it is idle")
	}

	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)
	if c.Idle() {
		t.Error("Idle stayed true with a mode change pending: the restart clock " +
			"would never be paid and the list would never start")
	}

	c.Step(0, 0, 1) // the restart clock
	if c.Idle() {
		t.Error("Idle is true while the Copper is running")
	}

	c.SetWritePtrHighAndMode(byte(StartStop) << 6)
	c.Step(0, 0, 1) // latch the change
	if !c.Idle() {
		t.Error("Idle stayed false after the Copper stopped and latched the mode")
	}
}

// The standard way to load a new list is to stop the Copper, write the list,
// then restart it: two OUTs to NextReg $62, mode 00 then mode 01 or 11. Both
// land between two Steps, because the CPU runs a whole frame before the render
// pass clocks the Copper at all.
//
// Latching the mode inside Step and comparing it against the previous clock's
// value cannot see that: at Step time the mode is 01 and the remembered mode is
// 01, so nothing looks changed and the list carries on from wherever it was.
// The FPGA compares last_state_s every 28 MHz clock and the two OUTs are far
// more than one clock apart, so it sees mode 00 in between and restarts.
//
// What has to survive between the write and the clock is therefore the fact
// that a change happened, not the mode it changed to.
func TestStopThenRestartRestartsTheList(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)
	c.Step(0, 0, 1) // the enabling clock
	c.pc = 500

	c.SetWritePtrHighAndMode(byte(StartStop) << 6)     // stop
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6) // ...and restart
	c.Step(0, 0, 1)                                    // exactly the restart clock

	if c.PC() != 0 {
		t.Errorf("pc after stop-then-restart = %d, want 0: the list must restart, "+
			"and both writes landed before this Step", c.PC())
	}
}

// The reverse order too: a restart followed by a stop leaves the Copper stopped
// with its list rewound, which is what the hardware's two clocks produce.
func TestRestartThenStopLeavesTheListRewoundAndStopped(t *testing.T) {
	c := New()
	c.SetWritePtrHighAndMode(byte(StartContinue) << 6)
	c.Step(0, 0, 1)
	c.pc = 500

	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)
	c.SetWritePtrHighAndMode(byte(StartStop) << 6)
	c.Step(0, 0, 4) // the restart clock, then the stopped arm returns

	if c.PC() != 0 {
		t.Errorf("pc = %d, want 0: the restart still happened", c.PC())
	}
	if !c.Idle() {
		t.Error("the Copper is not idle after being stopped")
	}
}
