package debugger

import "testing"

// The reverse cursor is what turns a recording into reverse debugging. It
// moves a position through the history ring, and every view reads the instant
// under the cursor instead of the live machine.
//
// Two boundaries carry all the risk. Running off the oldest end means the ring
// wrapped and the instruction asked for has been overwritten, which must be
// reported rather than silently clamped: silently clamping shows a real
// instruction that is not the one requested. Running off the newest end means
// leaving reverse mode and returning to the live machine, which is a state
// change the caller has to know about.

func ring(n int) *History {
	h := NewHistoryWide(n)
	for i := 0; i < n; i++ {
		h.Push(HistoryEntry{
			PC:    uint16(0x8000 + i),
			A:     byte(i),
			BC:    uint16(i * 2),
			Insns: uint64(i),
		})
	}
	return h
}

func TestEnteringReverseModeStartsAtTheNewestInstruction(t *testing.T) {
	c := NewReverseCursor(ring(10))
	if !c.Active() {
		t.Fatal("a cursor over a populated history is active")
	}
	e, ok := c.Current()
	if !ok || e.Insns != 9 {
		t.Errorf("cursor starts at insn %d, want the newest (9)", e.Insns)
	}
}

func TestAnEmptyHistoryHasNothingToStepBackThrough(t *testing.T) {
	c := NewReverseCursor(NewHistory(8))
	if c.Active() {
		t.Error("a cursor over an empty history must not claim to be active")
	}
	if _, ok := c.Current(); ok {
		t.Error("an empty history has no current instant")
	}
	if c.StepBack() == nil {
		t.Error("stepping back through an empty history must report why")
	}
}

func TestStepBackMovesOneInstructionAtATime(t *testing.T) {
	c := NewReverseCursor(ring(10))
	for want := 8; want >= 0; want-- {
		if err := c.StepBack(); err != nil {
			t.Fatalf("StepBack to insn %d: %v", want, err)
		}
		e, _ := c.Current()
		if e.Insns != uint64(want) {
			t.Fatalf("after StepBack, at insn %d, want %d", e.Insns, want)
		}
	}
}

// The oldest entry is a wall, not a clamp. Beyond it the ring has been
// overwritten, and showing the oldest surviving instruction as though it were
// the one requested would be a quiet lie.
func TestStepBackRefusesToGoPastTheOldestRecordedInstruction(t *testing.T) {
	c := NewReverseCursor(ring(3))
	for i := 0; i < 2; i++ {
		if err := c.StepBack(); err != nil {
			t.Fatalf("StepBack %d: %v", i, err)
		}
	}
	before, _ := c.Current()

	err := c.StepBack()
	if err == nil {
		t.Fatal("stepping past the oldest recorded instruction must report that history ran out")
	}
	after, _ := c.Current()
	if after.Insns != before.Insns {
		t.Error("a refused StepBack must leave the cursor where it was")
	}
}

// Walking forward again eventually reaches the present, and arriving there is
// how the caller knows to hand control back to the live machine.
func TestSteppingForwardToThePresentLeavesReverseMode(t *testing.T) {
	c := NewReverseCursor(ring(5))
	for i := 0; i < 3; i++ {
		if err := c.StepBack(); err != nil {
			t.Fatalf("StepBack: %v", err)
		}
	}
	if c.AtPresent() {
		t.Fatal("three steps back is not the present")
	}

	for i := 0; i < 3; i++ {
		if err := c.StepForward(); err != nil {
			t.Fatalf("StepForward: %v", err)
		}
	}
	if !c.AtPresent() {
		t.Error("stepping forward to the newest entry is the present again")
	}
	if err := c.StepForward(); err == nil {
		t.Error("stepping forward past the present must report it, not run the machine")
	}
}

func TestRunBackStopsAtTheMatchingInstant(t *testing.T) {
	cond, err := ParseCondition("a == 3")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	c := NewReverseCursor(ring(10))

	if err := c.RunBack(cond); err != nil {
		t.Fatalf("RunBack: %v", err)
	}
	e, _ := c.Current()
	if e.A != 3 {
		t.Errorf("RunBack stopped at A=%d, want 3", e.A)
	}
}

// Searching backwards must start BEFORE the cursor. Otherwise a condition that
// is already true stops immediately and the command appears to do nothing.
func TestRunBackDoesNotMatchTheInstantItStartsOn(t *testing.T) {
	cond, _ := ParseCondition("a == 9") // true at the newest entry
	c := NewReverseCursor(ring(10))

	if err := c.RunBack(cond); err == nil {
		t.Error("the only match was the starting instant, so RunBack must report no earlier match")
	}
}

func TestRunBackReportsWhenNothingMatches(t *testing.T) {
	cond, _ := ParseCondition("a == $FE")
	c := NewReverseCursor(ring(10))

	if err := c.RunBack(cond); err == nil {
		t.Fatal("no instant matches, so RunBack must say so")
	}
	if e, _ := c.Current(); e.Insns != 9 {
		t.Error("a RunBack that found nothing must leave the cursor where it was")
	}
}

// The refusal that keeps reverse mode honest.
func TestRunBackRefusesAConditionThatReadsMemory(t *testing.T) {
	cond, err := ParseCondition("read $5C78 == 0")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	c := NewReverseCursor(ring(10))

	err = c.RunBack(cond)
	if err == nil {
		t.Fatal("memory is not recorded per instruction, so a memory condition has no answer " +
			"going backwards and must be refused rather than evaluated against zero")
	}
	if e, _ := c.Current(); e.Insns != 9 {
		t.Error("a refused RunBack must not move the cursor")
	}
}

// A narrow ring never recorded the pair registers, so a condition on them
// cannot be answered either, and must fail the same way rather than matching
// against zeros.
func TestRunBackRefusesAConditionOnRegistersTheRingNeverRecorded(t *testing.T) {
	h := NewHistory(4) // narrow
	for i := 0; i < 4; i++ {
		h.Push(HistoryEntry{PC: uint16(0x8000 + i), A: byte(i), Insns: uint64(i)})
	}
	// "b", not "bc": the parser's vocabulary is the 8-bit registers plus IX
	// and IY, and an unparseable condition yields an empty one that matches
	// everything, which would make this test pass for the wrong reason.
	cond, err := ParseCondition("b == 0")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	c := NewReverseCursor(h)

	if err := c.RunBack(cond); err == nil {
		t.Error("a narrow ring did not record B, so the condition must be refused, " +
			"not matched against a zero that was never the register's value")
	}
}
