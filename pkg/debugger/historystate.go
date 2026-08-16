package debugger

// Evaluating conditions against a recorded instant, which is what lets the
// debugger run backwards to a register condition rather than only stepping.
//
// The recording holds registers and a stack window. It does not hold memory,
// and no amount of care changes that: memory is not captured per instruction
// because doing so would cost more than the machine costs to run. So a
// condition reading memory has no answer going backwards.
//
// It is refused rather than guessed. The alternative, which other reverse
// debuggers document, is that memory-reading breakpoints quietly stop being
// the breakpoints the user wrote: they still fire, or fail to, on a value that
// was never read. A confident wrong answer sends someone hunting a bug that is
// not there. An error message costs them one line and no false trail.

// HistoryState presents one recorded instant through the same interface the
// live machine uses, so a Condition does not know which it is evaluating.
type HistoryState struct {
	e    HistoryEntry
	wide bool
	// usedMem records that evaluation reached for memory, which history
	// cannot supply. Checked by the caller after Eval rather than returned
	// from ReadMem, because the State interface deliberately has no error
	// path: making ReadMem fallible would push a dead branch into the live
	// adapter, which can always answer.
	usedMem bool
	// missingReg names a register the condition asked for that this ring
	// never recorded. A narrow ring carries no pair registers, and matching
	// "bc == 0" against a field that was never filled is the same quiet lie
	// as evaluating a memory read against zero.
	missingReg string
}

// NewHistoryState wraps an entry from a wide ring, where the pair registers
// were recorded.
func NewHistoryState(e HistoryEntry) *HistoryState {
	return &HistoryState{e: e, wide: true}
}

// NewHistoryStateNarrow wraps an entry from a narrow ring, which recorded only
// PC, SP, A, F and the interrupt state. Asking it for BC reports unavailable
// rather than returning the zero those fields were never filled with.
func NewHistoryStateNarrow(e HistoryEntry) *HistoryState {
	return &HistoryState{e: e}
}

// Entry is the instant being presented.
func (s *HistoryState) Entry() HistoryEntry { return s.e }

// UsedMemory reports whether evaluation reached for memory, meaning the result
// must not be trusted and the condition must be refused.
func (s *HistoryState) UsedMemory() bool { return s.usedMem }

// MissingRegister names a register the evaluation asked for that this ring did
// not record, if any.
func (s *HistoryState) MissingRegister() (string, bool) {
	return s.missingReg, s.missingReg != ""
}

// Unanswerable reports whether evaluation reached for anything the recording
// does not hold, in which case its result means nothing.
func (s *HistoryState) Unanswerable() bool { return s.usedMem || s.missingReg != "" }

// note records an unavailable register and returns the zero-and-false the
// interface requires.
func (s *HistoryState) note(name string) (int64, bool) {
	if s.missingReg == "" {
		s.missingReg = name
	}
	return 0, false
}

// Reg answers from the recording, using the same names and the same pair
// decomposition as the live machine. A condition that means one thing forwards
// and another backwards would be worse than no reverse mode at all.
func (s *HistoryState) Reg(name string) (int64, bool) {
	switch name {
	case "pc":
		return int64(s.e.PC), true
	case "sp":
		return int64(s.e.SP), true
	case "a":
		return int64(s.e.A), true
	case "f":
		return int64(s.e.F), true
	case "iff1":
		return b2i(s.e.IFF1()), true
	case "iff2":
		return b2i(s.e.IFF2()), true
	case "halted":
		return b2i(s.e.Halted()), true
	case "im":
		return int64(s.e.IM()), true
	case "insns":
		return int64(s.e.Insns), true
	}

	// Everything below was only recorded by a wide ring.
	if !s.wide {
		return s.note(name)
	}
	switch name {
	case "b":
		return int64(s.e.BC >> 8), true
	case "c":
		return int64(s.e.BC & 0xFF), true
	case "d":
		return int64(s.e.DE >> 8), true
	case "e":
		return int64(s.e.DE & 0xFF), true
	case "h":
		return int64(s.e.HL >> 8), true
	case "l":
		return int64(s.e.HL & 0xFF), true
	case "ix":
		return int64(s.e.IX), true
	case "iy":
		return int64(s.e.IY), true
	}
	return s.note(name)
}

// ReadMem cannot be answered from a recording. It returns zero and records
// that it was asked, so the caller can refuse the condition rather than act on
// the zero.
func (s *HistoryState) ReadMem(uint16) byte {
	s.usedMem = true
	return 0
}

func b2i(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
