package debugger

import "errors"

// Reverse debugging: a cursor into the recorded history, so the debugger can
// walk backwards through instructions the machine has already executed.
//
// What this is, precisely, matters more than the name. The machine does not
// run backwards. The cursor moves through a ring of recorded instants, and
// while it is away from the present every view reads the instant under it
// rather than the live CPU. That gives exact registers, flags, the stack
// window and the branch that led to each instruction, going back as far as the
// ring holds.
//
// What it cannot give is memory, because memory is not recorded per
// instruction. Every place that limit bites is reported rather than papered
// over. See RunBack.

// Errors a caller is expected to show the user rather than treat as a fault.
var (
	// ErrNoHistory means nothing has been recorded, usually because the
	// history ring is disabled.
	ErrNoHistory = errors.New("no instruction history recorded")

	// ErrStartOfHistory means the ring does not reach back that far. The
	// instruction existed; the record of it has been overwritten.
	ErrStartOfHistory = errors.New("start of recorded history: the ring does not reach back further")

	// ErrAtPresent means the cursor is already at the live machine.
	ErrAtPresent = errors.New("already at the present instruction")

	// ErrNoEarlierMatch means the search reached the start of the ring
	// without the condition becoming true.
	ErrNoEarlierMatch = errors.New("no earlier instant in the recorded history matches")

	// ErrNotAnswerableInReverse means the condition depends on state that is
	// not recorded per instruction, so going backwards it has no answer.
	ErrNotAnswerableInReverse = errors.New(
		"condition reads state that is not recorded per instruction (memory, or a register this ring does not carry), " +
			"so it cannot be evaluated going backwards")
)

// ReverseCursor is a position within recorded history.
//
// Position is held as a distance back from the newest recorded instruction,
// rather than as an index into the ring. The ring keeps being written while
// the user reads it, so an index would drift under them: the entry at index 3
// is a different instruction a moment later. A distance from the newest entry
// is wrong in the same way, but a distance is at least meaningful to re-derive,
// and callers that care freeze the machine before stepping.
type ReverseCursor struct {
	h    *History
	back int // 0 = the newest recorded instruction
}

// NewReverseCursor positions a cursor at the newest recorded instruction.
func NewReverseCursor(h *History) *ReverseCursor {
	return &ReverseCursor{h: h}
}

// Active reports whether there is any recorded history to walk.
func (c *ReverseCursor) Active() bool { return c.h != nil && c.h.Len() > 0 }

// AtPresent reports whether the cursor is on the newest recorded instruction,
// which is where control returns to the live machine.
func (c *ReverseCursor) AtPresent() bool { return c.back == 0 }

// Distance is how many instructions back from the present the cursor sits.
func (c *ReverseCursor) Distance() int { return c.back }

// Current is the instant under the cursor.
func (c *ReverseCursor) Current() (HistoryEntry, bool) {
	if !c.Active() {
		return HistoryEntry{}, false
	}
	return c.h.At(c.h.Len() - 1 - c.back)
}

// State presents the current instant through the interface conditions use.
func (c *ReverseCursor) State() (*HistoryState, bool) {
	e, ok := c.Current()
	if !ok {
		return nil, false
	}
	return c.state(e), true
}

func (c *ReverseCursor) state(e HistoryEntry) *HistoryState {
	if c.h.Wide() {
		return NewHistoryState(e)
	}
	return NewHistoryStateNarrow(e)
}

// StepBack moves one instruction into the past.
//
// Reaching the oldest recorded instruction is an error rather than a clamp.
// Clamping would leave the cursor on a real instruction that is not the one
// asked for, and nothing on screen would say so.
func (c *ReverseCursor) StepBack() error {
	if !c.Active() {
		return ErrNoHistory
	}
	if c.back >= c.h.Len()-1 {
		return ErrStartOfHistory
	}
	c.back++
	return nil
}

// StepForward moves one instruction towards the present.
func (c *ReverseCursor) StepForward() error {
	if !c.Active() {
		return ErrNoHistory
	}
	if c.back == 0 {
		return ErrAtPresent
	}
	c.back--
	return nil
}

// ToPresent returns the cursor to the live machine.
func (c *ReverseCursor) ToPresent() { c.back = 0 }

// RunBack searches backwards for the most recent earlier instant satisfying
// the condition, and moves the cursor there.
//
// The search starts one instruction before the cursor. Starting at the cursor
// would match immediately whenever the condition is already true, and the
// command would look like it had done nothing.
//
// A condition that reaches for state the ring does not carry is refused, and
// the cursor does not move. This is the honest answer: memory is not recorded
// per instruction, so "read $5C78 == 0" has no value to test going backwards.
// Evaluating it against a zero would produce a breakpoint that silently stops
// being the one the user wrote, which sends people hunting bugs that are not
// there. One error message is cheaper.
func (c *ReverseCursor) RunBack(cond Condition) error {
	if !c.Active() {
		return ErrNoHistory
	}

	// The length is read ONCE. It was previously re-read in the loop bound and
	// again inside At, so the bound and the index came from two observations
	// of a ring that is still being written, and the scan could skip or
	// revisit entries. Nothing enforces the "freeze the machine first"
	// precondition, so this cannot rely on it.
	//
	// Answerability is then checked at every step rather than probed once.
	// Note this is NOT because evaluation short circuits: binOp.eval computes
	// both operands eagerly (condition.go), so a memory read is reached on the
	// first instant regardless of the other operand. An earlier version of
	// this comment claimed the opposite and was wrong. Checking per step is
	// still correct, and costs nothing, because which registers a narrow ring
	// can answer does not vary between entries either.
	n := c.h.Len()
	for back := c.back + 1; back <= n-1; back++ {
		e, ok := c.h.At(n - 1 - back)
		if !ok {
			break
		}
		s := c.state(e)
		match := cond.Eval(s)
		if s.Unanswerable() {
			return ErrNotAnswerableInReverse
		}
		if match {
			c.back = back
			return nil
		}
	}
	return ErrNoEarlierMatch
}
