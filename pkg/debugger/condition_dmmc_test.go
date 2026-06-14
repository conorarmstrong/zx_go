package debugger

import "testing"

// TestConditionDMMCPredicate verifies the condition grammar accepts a
// `dmmc` predicate and resolves it through State.Reg. This lets a
// breakpoint fire only when the divMMC ROM is paged in
// (`set-breakpoint $XXXX if dmmc == 1`) — the faithful replacement for
// the `if iy < $8000` heuristic used to dodge low-PC bank-aliasing.
func TestConditionDMMCPredicate(t *testing.T) {
	c, err := ParseCondition("dmmc == 1")
	if err != nil {
		t.Fatalf("ParseCondition(dmmc == 1): %v", err)
	}
	if !c.Eval(fakeState{regs: map[string]int64{"dmmc": 1}}) {
		t.Error("dmmc == 1 should be true when dmmc resolves to 1")
	}
	if c.Eval(fakeState{regs: map[string]int64{"dmmc": 0}}) {
		t.Error("dmmc == 1 should be false when dmmc resolves to 0")
	}
}

// TestConditionDivMMCStateFields verifies the rest of the divMMC paging
// state is addressable in conditions, matching the `get-divmmc` field
// names so a breakpoint can target a precise paging mode, e.g.
// `if automap == 1 && conmem == 0`.
func TestConditionDivMMCStateFields(t *testing.T) {
	for _, name := range []string{"automap", "mapram", "conmem"} {
		c, err := ParseCondition(name + " == 1")
		if err != nil {
			t.Fatalf("ParseCondition(%s == 1): %v", name, err)
		}
		if !c.Eval(fakeState{regs: map[string]int64{name: 1}}) {
			t.Errorf("%s == 1 should be true when %s resolves to 1", name, name)
		}
		if c.Eval(fakeState{regs: map[string]int64{name: 0}}) {
			t.Errorf("%s == 1 should be false when %s resolves to 0", name, name)
		}
	}
}
