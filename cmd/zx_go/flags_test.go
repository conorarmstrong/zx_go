package main

import "testing"

func TestParseAddr(t *testing.T) {
	cases := map[string]uint16{
		"$0D6B":   0x0D6B,
		"0x0D6B":  0x0D6B,
		"0X0D6B":  0x0D6B,
		"$ffff":   0xFFFF,
		"100":     100,
		" $1234 ": 0x1234,
	}
	for in, want := range cases {
		got, err := parseAddr(in)
		if err != nil {
			t.Errorf("parseAddr(%q) err=%v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseAddr(%q) = %#x, want %#x", in, got, want)
		}
	}
}

func TestParsePCRange(t *testing.T) {
	cases := []struct {
		in           string
		wantS, wantE uint16
		hasErr       bool
	}{
		{"$0D6B-$0D80", 0x0D6B, 0x0D80, false},
		{"0x1000-0x1100", 0x1000, 0x1100, false},
		{"$0D6B-$0D6B", 0x0D6B, 0x0D6B, false},
		{"0-65535", 0, 65535, false},
		{"$0D80-$0D6B", 0, 0, true}, // end < start
		{"$0D6B", 0, 0, true},       // no dash
		{"bogus-$0D6B", 0, 0, true},
	}
	for _, c := range cases {
		s, e, err := parsePCRange(c.in)
		if c.hasErr {
			if err == nil {
				t.Errorf("parsePCRange(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePCRange(%q) err=%v", c.in, err)
			continue
		}
		if s != c.wantS || e != c.wantE {
			t.Errorf("parsePCRange(%q) = (%#x, %#x), want (%#x, %#x)",
				c.in, s, e, c.wantS, c.wantE)
		}
	}
}
