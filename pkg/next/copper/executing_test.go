package copper

import "testing"

// Instruction encodings, per device/copper.vhd: MOVE is bit 15 clear with the
// 7-bit register in 14-8 and the value in 7-0; a zero register field is NOOP;
// HALT is the reserved all-ones word.
const (
	noop uint16 = 0x0000
	halt uint16 = 0xFFFF
)

func move(reg, val byte) uint16 { return uint16(reg)<<8 | uint16(val) }

// A copper MOVE reaches the NextReg dispatcher through the same WriteReg the
// CPU reaches through ports $243B/$253B, so a debugger watching a register
// cannot tell the two apart from the call alone. Executing and PC let it: a
// hit reported during a MOVE names the copper instruction that caused it
// rather than whatever address the Z80 happened to be at.

// recordingWriter captures the copper's self-reported state at the instant of
// each MOVE, which is the only instant a watch hook gets to look.
type recordingWriter struct {
	c         *Copper
	executing []bool
	pcs       []uint16
}

func (w *recordingWriter) WriteReg(reg, val byte) {
	w.executing = append(w.executing, w.c.Executing())
	w.pcs = append(w.pcs, w.c.PC())
}

func TestExecutingIsTrueDuringAMove(t *testing.T) {
	c := New()
	w := &recordingWriter{c: c}
	c.SetRegWriter(w)

	// Two MOVEs then a HALT, loaded high-byte-first as NR$60 delivers them.
	for _, inst := range []uint16{move(0x12, 0x34), move(0x56, 0x78), halt} {
		c.WriteData(byte(inst >> 8))
		c.WriteData(byte(inst & 0xFF))
	}
	c.SetWritePtrLow(0)
	c.SetWritePtrHighAndMode(byte(StartFromZero)<<6)

	c.Step(0, 511, 64)

	if len(w.executing) != 2 {
		t.Fatalf("MOVEs seen = %d, want 2", len(w.executing))
	}
	for i, ex := range w.executing {
		if !ex {
			t.Errorf("MOVE %d: Executing() = false during the write, want true", i)
		}
	}
}

// Outside a MOVE the copper is not executing. A CPU write that lands between
// Steps must not be attributed to the copper.
func TestExecutingIsFalseOutsideAMove(t *testing.T) {
	c := New()
	if c.Executing() {
		t.Error("Executing() = true on a copper that has never run")
	}

	w := &recordingWriter{c: c}
	c.SetRegWriter(w)
	inst := move(0x12, 0x34)
	c.WriteData(byte(inst >> 8))
	c.WriteData(byte(inst & 0xFF))
	c.WriteData(byte(halt >> 8))
	c.WriteData(byte(halt & 0xFF))
	c.SetWritePtrLow(0)
	c.SetWritePtrHighAndMode(byte(StartFromZero)<<6)
	c.Step(0, 511, 64)

	if c.Executing() {
		t.Error("Executing() = true after Step returned")
	}
}

// PC names the instruction doing the writing, not the one after it. The
// increment happens after the write, so a hit reported at pc+1 would point a
// user at the wrong copper instruction.
func TestPCDuringMoveNamesTheMoveItself(t *testing.T) {
	c := New()
	w := &recordingWriter{c: c}
	c.SetRegWriter(w)

	for _, inst := range []uint16{noop, move(0x12, 0x34), halt} {
		c.WriteData(byte(inst >> 8))
		c.WriteData(byte(inst & 0xFF))
	}
	c.SetWritePtrLow(0)
	c.SetWritePtrHighAndMode(byte(StartFromZero)<<6)

	c.Step(0, 511, 64)

	if len(w.pcs) != 1 {
		t.Fatalf("MOVEs seen = %d, want 1", len(w.pcs))
	}
	if w.pcs[0] != 1 {
		t.Errorf("PC() during the MOVE = %d, want 1 (the MOVE is instruction 1)", w.pcs[0])
	}
}
