package main

import "testing"

// The wildcard form finds which physical bank backs a logical sysvar
// when the OS remaps the slot.
func TestParseRAMWriteTraceSpec(t *testing.T) {
	cases := []struct {
		spec   string
		bank   int
		lo, hi uint16
		ok     bool
	}{
		{"05:2000-2FFF", 5, 0x2000, 0x2FFF, true},
		{"*:1B6C-1B6D", -1, 0x1B6C, 0x1B6D, true},
		{"0A:0000-3FFF", 10, 0x0000, 0x3FFF, true},
		{"garbage", 0, 0, 0, false},
		{"*:zz-aa", 0, 0, 0, false},
	}
	for _, c := range cases {
		bank, lo, hi, ok := parseRAMWriteTraceSpec(c.spec)
		if ok != c.ok || (ok && (bank != c.bank || lo != c.lo || hi != c.hi)) {
			t.Errorf("%q: got (%d,%04X,%04X,%v), want (%d,%04X,%04X,%v)",
				c.spec, bank, lo, hi, ok, c.bank, c.lo, c.hi, c.ok)
		}
	}
}
