package main

import (
	"strings"
	"testing"
)

// TestFormatHexDump verifies the classic hexdump layout: per-row address
// column, up to 16 space-separated hex bytes, and a printable-ASCII
// gutter (non-printables shown as '.'). A visual upgrade over the flat
// `get-memory` byte list (jnext's memory hex view, as a command).
func TestFormatHexDump(t *testing.T) {
	data := []byte{'H', 'i', '!', 0x00, 0xFF, 0x7F, 'A', 'Z'}
	out := formatHexDump(0x4000, data)

	if !strings.Contains(out, "$4000") {
		t.Errorf("missing address column:\n%s", out)
	}
	if !strings.Contains(out, "48 69 21") { // 'H' 'i' '!'
		t.Errorf("missing hex bytes:\n%s", out)
	}
	// ASCII gutter: printable shown literally, others as '.'
	if !strings.Contains(out, "Hi!...AZ") {
		t.Errorf("missing/incorrect ASCII gutter (want Hi!...AZ):\n%s", out)
	}
}

// TestFormatHexDumpRowWrap verifies a second address row appears past 16
// bytes (one row per 16) so long dumps stay aligned.
func TestFormatHexDumpRowWrap(t *testing.T) {
	data := make([]byte, 20) // 16 + 4 → two rows
	out := formatHexDump(0x8000, data)
	if !strings.Contains(out, "$8000") || !strings.Contains(out, "$8010") {
		t.Errorf("expected rows at $8000 and $8010:\n%s", out)
	}
}
