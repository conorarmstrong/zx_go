package debugger

import "testing"

// Reverse debugging evaluates conditions against a recorded instant rather
// than the live machine, which is what makes "run backwards until A == 0"
// possible. The recording holds registers and a stack window and nothing else,
// so a condition that reads memory cannot be answered.
//
// The design decision worth pinning: it must be REFUSED, not guessed. Other
// implementations of reverse debugging document that memory-reading conditions
// simply "are not evaluated correctly" in reverse, which means a breakpoint
// silently stops being the breakpoint the user wrote. Returning a wrong answer
// confidently is the worst option available; saying "I cannot answer that
// going backwards" costs the user one error message and no false trails.

func entry() HistoryEntry {
	return HistoryEntry{
		PC: 0x8000, SP: 0xFF00,
		A: 0x3C, F: 0x41,
		BC: 0x1234, DE: 0x5678, HL: 0x9ABC,
		IX: 0xDEAD, IY: 0xBEEF,
		AltAF: 0x1111, AltBC: 0x2222, AltDE: 0x3333, AltHL: 0x4444,
		IFFIM: PackIFFIM(true, false, false, 2),
		Insns: 4242,
	}
}

func TestHistoryStateAnswersTheRegistersItRecorded(t *testing.T) {
	s := NewHistoryState(entry())

	for _, tc := range []struct {
		name string
		want int64
	}{
		{"pc", 0x8000}, {"sp", 0xFF00},
		{"a", 0x3C}, {"f", 0x41},
		{"b", 0x12}, {"c", 0x34},
		{"d", 0x56}, {"e", 0x78},
		{"h", 0x9A}, {"l", 0xBC},
		{"ix", 0xDEAD}, {"iy", 0xBEEF},
		{"iff1", 1}, {"iff2", 0},
		{"im", 2}, {"halted", 0},
	} {
		got, ok := s.Reg(tc.name)
		if !ok {
			t.Errorf("Reg(%q) not available from history", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("Reg(%q) = %#x, want %#x", tc.name, got, tc.want)
		}
	}
}

// The register pairs have to decompose the same way the live adapter does, or
// a condition means one thing forwards and another backwards.
func TestHistoryStateSplitsRegisterPairsTheSameWayTheLiveMachineDoes(t *testing.T) {
	s := NewHistoryState(HistoryEntry{BC: 0xAB12, DE: 0xCD34, HL: 0xEF56})
	for _, tc := range []struct {
		name string
		want int64
	}{
		{"b", 0xAB}, {"c", 0x12},
		{"d", 0xCD}, {"e", 0x34},
		{"h", 0xEF}, {"l", 0x56},
	} {
		if got, _ := s.Reg(tc.name); got != tc.want {
			t.Errorf("Reg(%q) = %#x, want %#x", tc.name, got, tc.want)
		}
	}
}

// A narrow ring records no pair registers, so asking for them must report
// unavailable rather than returning a confident zero.
func TestHistoryStateReportsUnavailableRegistersRatherThanZero(t *testing.T) {
	s := NewHistoryStateNarrow(HistoryEntry{PC: 0x8000, A: 1})

	if _, ok := s.Reg("pc"); !ok {
		t.Error("pc is recorded even in a narrow ring and must be available")
	}
	for _, name := range []string{"b", "c", "ix", "iy"} {
		if _, ok := s.Reg(name); ok {
			t.Errorf("Reg(%q) reported a value from a narrow ring, which never recorded it", name)
		}
	}
}

// The load-bearing refusal.
func TestHistoryStateFlagsAConditionThatReadsMemory(t *testing.T) {
	s := NewHistoryState(entry())
	if s.UsedMemory() {
		t.Fatal("nothing has read memory yet")
	}

	_ = s.ReadMem(0x5C78)

	if !s.UsedMemory() {
		t.Error("a memory read while stepping backwards must be recorded, so the caller can " +
			"refuse the condition instead of acting on an invented value")
	}
}

// End to end: a register condition evaluates against a recorded instant, and a
// memory condition is detectable as unanswerable.
func TestConditionsEvaluateAgainstARecordedInstant(t *testing.T) {
	c, err := ParseCondition("pc == $8000 && a < $80")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	s := NewHistoryState(entry())
	if !c.Eval(s) {
		t.Error("a register-only condition must evaluate against history")
	}
	if s.UsedMemory() {
		t.Error("a register-only condition must not be reported as touching memory")
	}

	mem, err := ParseCondition("read $5C78 == 0")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	s2 := NewHistoryState(entry())
	_ = mem.Eval(s2)
	if !s2.UsedMemory() {
		t.Error("a memory condition must be reported as unanswerable in reverse")
	}
}

// The ROM bank IS recorded in every entry, and the live machine answers "bank",
// so refusing it going backwards spends the refusal on state that was captured.
// The refusal has to stay reserved for what genuinely was not recorded, or it
// stops meaning anything.
func TestHistoryStateAnswersTheRecordedROMBank(t *testing.T) {
	s := NewHistoryState(HistoryEntry{PC: 0x8000, ROMBnk: 3})

	got, ok := s.Reg("bank")
	if !ok {
		t.Fatal("bank is recorded in every entry and must be answerable going backwards")
	}
	if got != 3 {
		t.Errorf("Reg(\"bank\") = %d, want 3", got)
	}
	if s.Unanswerable() {
		t.Error("answering a recorded field must not mark the evaluation unanswerable")
	}
}

// A narrow ring records the bank too, so it is answerable there as well.
func TestANarrowRingStillAnswersTheROMBank(t *testing.T) {
	s := NewHistoryStateNarrow(HistoryEntry{ROMBnk: 2})
	if got, ok := s.Reg("bank"); !ok || got != 2 {
		t.Errorf("narrow Reg(\"bank\") = %d, ok=%v, want 2, true", got, ok)
	}
}
