package debugger

import "testing"

// Covers the residual condition-evaluator branches — `peek` alias,
// `0x` hex literal form, strict `<`/`>` comparison eval,
// empty-condition String(), and a register absent from State.

func TestCondition_PeekAlias(t *testing.T) {
	s := fakeState{mem: map[uint16]byte{0x4000: 0x7E}}
	cond, err := ParseCondition("peek $4000 == $7E")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	if !cond.Eval(s) {
		t.Errorf("peek $4000 == $7E: want true")
	}
	// String() reprints `read` (peek is an input alias for the same node).
	if got := cond.String(); got != "read $4000 == $7E" {
		t.Errorf("String() = %q, want %q", got, "read $4000 == $7E")
	}
}

func TestCondition_0xHexLiteral(t *testing.T) {
	s := fakeState{regs: map[string]int64{"pc": 0x1234}}
	cond, err := ParseCondition("pc == 0x1234")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	if !cond.Eval(s) {
		t.Errorf("pc == 0x1234: want true")
	}
}

func TestCondition_StrictLessGreater(t *testing.T) {
	s := fakeState{regs: map[string]int64{"a": 0x40, "b": 0x40}}
	cases := []struct {
		expr string
		want bool
	}{
		{"a < $41", true},
		{"a < $40", false}, // strict: equal is not less
		{"a > $3F", true},
		{"a > $40", false}, // strict: equal is not greater
		{"a < b", false},   // register vs register, equal
	}
	for _, c := range cases {
		cond, err := ParseCondition(c.expr)
		if err != nil {
			t.Fatalf("ParseCondition(%q): %v", c.expr, err)
		}
		if got := cond.Eval(s); got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestCondition_EmptyString(t *testing.T) {
	var c Condition
	if got := c.String(); got != "" {
		t.Errorf("zero Condition.String() = %q, want empty", got)
	}
}

// A register that the State doesn't know about reads as 0 (Reg
// returns ok=false → eval yields 0).
func TestCondition_RegisterAbsentFromState(t *testing.T) {
	s := fakeState{regs: map[string]int64{}} // empty
	cond, err := ParseCondition("bank == 0")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	if !cond.Eval(s) {
		t.Errorf("absent register `bank` should read 0 → bank == 0 true")
	}
}
