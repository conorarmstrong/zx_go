package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/copper"
)

// TestFormatCopperDisasm verifies the Copper program disassembler:
// a header with cursor + decoded start-mode, then one line per
// instruction (MOVE NR$rr,$vv / WAIT line=Y hpos=X / HALT), stopping
// at the first HALT so we don't dump 1024 trailing NOOPs.
func TestFormatCopperDisasm(t *testing.T) {
	prog := []copper.Instruction{
		{Op: copper.OpMOVE, Reg: 0x40, Val: 0x07},
		{Op: copper.OpWAIT, Y: 192, X: 0},
		{Op: copper.OpHALT},
		{Op: copper.OpMOVE, Reg: 0x12, Val: 0xFF}, // past HALT — must NOT render
	}
	rd := func(i uint16) copper.Instruction {
		if int(i) < len(prog) {
			return prog[i]
		}
		return copper.Instruction{Op: copper.OpNOOP}
	}

	out := formatCopperDisasm(rd, 0x00A, copper.StartOnVBL, 64)

	for _, want := range []string{
		"Copper", "$00A", "VBL", // header: title, cursor, decoded mode
		"MOVE", "NR$40", "$07", // MOVE decoded
		"WAIT", "192", // WAIT scanline
		"HALT",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("disasm missing %q\n---\n%s", want, out)
		}
	}
	// Must stop at HALT — the post-HALT MOVE to NR$12 must not appear.
	if strings.Contains(out, "NR$12") {
		t.Errorf("disasm continued past HALT (rendered NR$12):\n%s", out)
	}
}
