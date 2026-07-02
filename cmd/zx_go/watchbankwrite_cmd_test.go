package main

import "testing"

// TestParsePoolWriteSpec covers the --watch-bank-write spec parser.
func TestParsePoolWriteSpec(t *testing.T) {
	got := parsePoolWriteSpec("111:$201C-$202B,5:0x10-0x10, 7:2000 ")
	if len(got) != 3 {
		t.Fatalf("parsed %d entries, want 3: %+v", len(got), got)
	}
	if got[0] != (poolWriteWatch{bank: 111, lo: 0x201C, hi: 0x202B}) {
		t.Errorf("entry0 = %+v", got[0])
	}
	if got[1] != (poolWriteWatch{bank: 5, lo: 0x10, hi: 0x10}) {
		t.Errorf("entry1 = %+v", got[1])
	}
	// No "-HI" → hi defaults to lo.
	if got[2] != (poolWriteWatch{bank: 7, lo: 0x2000, hi: 0x2000}) {
		t.Errorf("entry2 = %+v", got[2])
	}
}

func TestParsePoolWriteSpec_RejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "nobank", "x:1-2", "111", ":10-20"} {
		if got := parsePoolWriteSpec(s); len(got) != 0 {
			t.Errorf("parsePoolWriteSpec(%q) = %+v, want empty", s, got)
		}
	}
}
