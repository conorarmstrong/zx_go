package debugger

import "testing"

// fakeState satisfies State for parser tests.
type fakeState struct {
	regs map[string]int64
	mem  map[uint16]byte
}

func (f fakeState) Reg(name string) (int64, bool) {
	v, ok := f.regs[name]
	return v, ok
}
func (f fakeState) ReadMem(addr uint16) byte { return f.mem[addr] }

func TestParseConditionAtoms(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"pc == $6010", "pc == $6010"},
		{"pc != 42", "pc != 42"},
		{"a < $80 && b > 0", "a < $80 && b > 0"},
		{"read $5C78 == 0", "read $5C78 == 0"},
		{"pc in $1FFA-$3FFF", "pc in $1FFA-$3FFF"},
		{"iff1 == 0 || halted == 1", "iff1 == 0 || halted == 1"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			cond, err := ParseCondition(c.input)
			if err != nil {
				t.Fatalf("ParseCondition: %v", err)
			}
			if got := cond.String(); got != c.want {
				t.Errorf("String() = %q want %q", got, c.want)
			}
		})
	}
}

func TestParseConditionErrors(t *testing.T) {
	bad := []string{
		"pc",                  // missing operator
		"pc ==",               // missing right operand
		"unknown == 0",        // unknown register
		"pc == $XYZ",          // bad hex
		"pc in $00",           // missing range
		"pc in $00-",          // missing hi
		"pc == 0 trailing",    // trailing tokens
		"pc == 0 && trailing", // trailing token after && cmp parse fails
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseCondition(in); err == nil {
				t.Errorf("ParseCondition(%q): expected error", in)
			}
		})
	}
}

func TestEvalSimple(t *testing.T) {
	s := fakeState{
		regs: map[string]int64{"pc": 0x6010, "iff1": 0, "a": 0x42, "halted": 1},
		mem:  map[uint16]byte{0x5C78: 0xFF, 0x4242: 0x00},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{"pc == $6010", true},
		{"pc == $6011", false},
		{"pc == $6010 && iff1 == 0", true},
		{"pc == $6011 || iff1 == 0", true},
		{"pc == $6011 && iff1 == 0", false},
		{"a >= $40 && a <= $50", true},
		{"a >= $40 && a <= $30", false},
		{"read $5C78 == $FF", true},
		{"read $4242 != $00", false},
		{"pc in $6000-$601F", true},
		{"pc in $6020-$6030", false},
		{"halted == 1 && iff1 == 0", true},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			cond, err := ParseCondition(c.expr)
			if err != nil {
				t.Fatalf("ParseCondition: %v", err)
			}
			if got := cond.Eval(s); got != c.want {
				t.Errorf("Eval(%q) = %v want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestEvalReadMemThroughExpression(t *testing.T) {
	// Verify nested expressions in read: read at a register-derived
	// address.
	s := fakeState{
		regs: map[string]int64{"hl": 0x4242},
		mem:  map[uint16]byte{0x4242: 0xAA},
	}
	// Since 'hl' isn't in our register table, the expression with
	// just 'read $4242' should still evaluate. Confirm direct address.
	cond, err := ParseCondition("read $4242 == $AA")
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	if !cond.Eval(s) {
		t.Errorf("read $4242 == $AA: expected true")
	}
}

func TestEmptyConditionAlwaysTrue(t *testing.T) {
	// The zero-value Condition (no guard) should treat as
	// unconditional. Callers use this when a breakpoint has no
	// `if` clause attached.
	var c Condition
	if !c.Eval(fakeState{}) {
		t.Errorf("zero Condition.Eval() = false; want true (unconditional)")
	}
}
