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

func TestDecodeHALT(t *testing.T) {
	if Decode(0xFFFF).Op != OpHALT {
		t.Errorf("0xFFFF should decode as HALT")
	}
}

func TestDecodeNOOP(t *testing.T) {
	if Decode(0x0000).Op != OpNOOP {
		t.Errorf("0x0000 should decode as NOOP")
	}
}

func TestWriteDataLatchesPair(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0x10)
	c.WriteData(0x42) // high byte staged
	c.WriteData(0x55) // commits
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
	// HALT at index 1.
	c.WriteData(0xFF)
	c.WriteData(0xFF)

	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	// Start from zero (mode 1).
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)
	// maxInstr=2 lets MOVE then HALT both execute in one Step.
	c.Step(0, 0, 2)

	if len(rw.writes) != 1 || rw.writes[0].reg != 0x07 || rw.writes[0].val != 0x02 {
		t.Errorf("MOVE not executed: writes = %+v", rw.writes)
	}
	if !c.stopped {
		t.Errorf("HALT should stop the copper")
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

	// Step with budget=1 — should execute exactly the first MOVE.
	c.Step(0, 0, 1)
	if len(rw.writes) != 1 || rw.writes[0].val != 0x10 {
		t.Errorf("Step(_,_,1) executed %d MOVEs, want exactly 1; first val = %#x, want 0x10",
			len(rw.writes), rw.writes[0].val)
	}
	// Step again — second MOVE.
	c.Step(0, 0, 1)
	if len(rw.writes) != 2 || rw.writes[1].val != 0x11 {
		t.Errorf("second Step: expected 2 MOVEs total; got %+v", rw.writes)
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
	// Program at index 0: MOVE 0x60, 0xAB (re-enters); HALT.
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
	if !c.stopped {
		t.Errorf("Copper should have run MOVE then HALT and stopped")
	}
}

func TestCursorWraps(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0xFF)
	c.SetWritePtrHighAndMode(0x03) // high 2 bits both set -> cursor 0x3FF
	if c.Cursor() != 0x3FF {
		t.Errorf("Cursor after high-write = %#x, want 0x3FF", c.Cursor())
	}
	// Write past end — pointer wraps.
	c.WriteData(0x00)
	c.WriteData(0x00) // commit at 0x3FF -> 0x400, wraps to 0
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
	// x=63, y=511 — the HALT-encoded WAIT 0xFFFF.
	got := Decode(0xFFFF)
	// 0xFFFF is explicitly HALT.
	if got.Op != OpHALT {
		t.Errorf("$FFFF Op = %v, want HALT (encoded WAIT max)", got.Op)
	}
	// x=63, y=510 → not HALT.
	w := uint16(0x8000) | (uint16(63) << 9) | 510
	got = Decode(w)
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
