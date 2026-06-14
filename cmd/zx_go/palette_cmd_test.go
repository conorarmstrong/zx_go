package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// TestFormatPaletteDump verifies the palette inspector dumps the
// active 256-entry table as a 9-bit-hex grid (16 per row, row-address
// labelled) plus the decoded RGB for a sampled entry. jnext's GUI has
// a palette viewer; this is the scriptable, decoded equivalent.
func TestFormatPaletteDump(t *testing.T) {
	p := palette.New()
	p.Set(0, 0x000)    // black
	p.Set(1, 0x1FF)    // all bits set
	p.Set(0x10, 0x1C0) // pure red (RRR=111)

	out := formatPaletteDump(p, 256)

	for _, want := range []string{
		"Palette", // header
		"$00:",    // row-address label for first row
		"$10:",    // second-displayed row at index 16
		"1FF",     // entry 1's 9-bit value
		"1C0",     // entry 0x10's value
	} {
		if !strings.Contains(out, want) {
			t.Errorf("palette dump missing %q\n---\n%s", want, out)
		}
	}
}

// TestFormatPaletteDump_Count honours the entry count (used to cap
// output) — a count of 16 yields exactly one row label set.
func TestFormatPaletteDump_Count(t *testing.T) {
	p := palette.New()
	out := formatPaletteDump(p, 16)
	if strings.Contains(out, "$10:") {
		t.Errorf("count=16 should not render a row at index 16:\n%s", out)
	}
}
