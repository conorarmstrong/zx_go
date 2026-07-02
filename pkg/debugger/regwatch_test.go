package debugger

import "testing"

// fakeRegSrc implements a moving register file for RegWatch tests.
// Tests advance the file and call CheckRegWatches to drive transitions.
type fakeRegSrc struct {
	vals map[string]int64
	mem  map[uint16]byte
}

func (f *fakeRegSrc) Reg(name string) (int64, bool) {
	v, ok := f.vals[name]
	return v, ok
}

func (f *fakeRegSrc) ReadMem(addr uint16) byte { return f.mem[addr] }

func TestRegWatch_FiresOnAnyChange(t *testing.T) {
	w := NewRegWatchSet()
	w.Add(RegWatch{Reg: "ix"})
	src := &fakeRegSrc{vals: map[string]int64{"ix": 0xFFFF}}
	// Prime the watch with the current value (no fire on first eval).
	if w.Check(src) {
		t.Fatalf("watch fired on prime")
	}
	// No change -> no fire.
	if w.Check(src) {
		t.Fatalf("watch fired without change")
	}
	src.vals["ix"] = 0xCCE7
	if !w.Check(src) {
		t.Fatalf("watch did not fire on change")
	}
}

func TestRegWatch_HonorsToVal(t *testing.T) {
	w := NewRegWatchSet()
	w.Add(RegWatch{Reg: "iff1", HasTo: true, To: 1})
	src := &fakeRegSrc{vals: map[string]int64{"iff1": 0}}
	w.Check(src) // prime
	// Change to 0 (no-op): no fire.
	if w.Check(src) {
		t.Fatalf("fired when value unchanged")
	}
	// Change to 1: fire.
	src.vals["iff1"] = 1
	if !w.Check(src) {
		t.Fatalf("did not fire on transition to To value")
	}
	// Subsequent stay-at-To: no re-fire.
	if w.Check(src) {
		t.Fatalf("fired again at stable To value")
	}
	// Change back to 0: no fire (we only fire on To match).
	src.vals["iff1"] = 0
	if w.Check(src) {
		t.Fatalf("fired on transition AWAY from To")
	}
}

func TestRegWatch_HonorsFromVal(t *testing.T) {
	w := NewRegWatchSet()
	w.Add(RegWatch{Reg: "ix", HasFrom: true, From: 0xFFFF})
	src := &fakeRegSrc{vals: map[string]int64{"ix": 0xFFFF}}
	w.Check(src) // prime
	// The primed value (0xFFFF) equals From, so the transition away
	// from it should fire.
	src.vals["ix"] = 0x1234
	if !w.Check(src) {
		t.Fatalf("did not fire on transition away from From")
	}
	// Subsequent change while old != From: no fire.
	src.vals["ix"] = 0xABCD
	if w.Check(src) {
		t.Fatalf("fired on change with neither end equal to From")
	}
}

func TestRegWatch_UnknownRegIgnored(t *testing.T) {
	w := NewRegWatchSet()
	w.Add(RegWatch{Reg: "bogus"})
	src := &fakeRegSrc{vals: map[string]int64{}}
	if w.Check(src) {
		t.Fatalf("unknown reg fired")
	}
}

func TestRegWatch_RemoveByReg(t *testing.T) {
	w := NewRegWatchSet()
	w.Add(RegWatch{Reg: "a"})
	w.Add(RegWatch{Reg: "b"})
	w.Remove("a")
	src := &fakeRegSrc{vals: map[string]int64{"a": 0, "b": 0}}
	w.Check(src)
	src.vals["a"] = 1
	if w.Check(src) {
		t.Fatalf("removed watch still fired")
	}
	src.vals["b"] = 1
	if !w.Check(src) {
		t.Fatalf("retained watch did not fire")
	}
}

func TestRegWatchSet_ListClearEmpty(t *testing.T) {
	s := NewRegWatchSet()
	if !s.Empty() {
		t.Error("fresh set should be Empty")
	}
	s.Add(RegWatch{Reg: "a"})
	s.Add(RegWatch{Reg: "b", HasTo: true, To: 5})
	if s.Empty() {
		t.Error("set with two watches reports Empty")
	}
	// List returns insertion order.
	list := s.List()
	if len(list) != 2 || list[0].Reg != "a" || list[1].Reg != "b" {
		t.Fatalf("List = %+v, want [a b] in order", list)
	}
	s.Clear()
	if !s.Empty() {
		t.Error("after Clear, set should be Empty")
	}
	if len(s.List()) != 0 {
		t.Error("after Clear, List should be empty")
	}
}

func TestRegWatch_FormatLine(t *testing.T) {
	cases := []struct {
		w    RegWatch
		want string
	}{
		{RegWatch{Reg: "ix"}, "ix (any change)"},
		{RegWatch{Reg: "iff1", HasTo: true, To: 1}, "iff1 -> 1"},
		{RegWatch{Reg: "ix", HasFrom: true, From: 0xFFFF}, "ix from $FFFF"},
		{RegWatch{Reg: "ix", HasFrom: true, From: 0xFFFF, HasTo: true, To: 0}, "ix from $FFFF -> 0"},
	}
	for _, tc := range cases {
		if got := tc.w.FormatLine(); got != tc.want {
			t.Errorf("FormatLine got %q want %q", got, tc.want)
		}
	}
}
