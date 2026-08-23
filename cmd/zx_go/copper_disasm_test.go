package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/copper"
)

// TestFormatCopperDisasm verifies the Copper program disassembler:
// a header with cursor + decoded start-mode, then one line per
// instruction (MOVE NR$rr,$vv / WAIT line=Y hpos=X), stopping at the
// end-of-list terminator so we don't dump 1024 trailing NOOPs.
//
// The terminator is a WAIT for line 511, which is what $FFFF decodes to.
// The hardware has no HALT opcode: $FFFF simply parks, because vcount
// never reaches 511 (device/copper.vhd). The disassembler says so rather
// than inventing a mnemonic the silicon does not have.
func TestFormatCopperDisasm(t *testing.T) {
	prog := []copper.Instruction{
		{Op: copper.OpMOVE, Reg: 0x40, Val: 0x07},
		{Op: copper.OpWAIT, Y: 192, X: 0},
		{Op: copper.OpWAIT, Y: 511, X: 63}, // $FFFF: the end-of-list terminator
		{Op: copper.OpMOVE, Reg: 0x12, Val: 0xFF}, // past it — must NOT render
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
		"511", "parks", // the terminator, named for what it does
	} {
		if !strings.Contains(out, want) {
			t.Errorf("disasm missing %q\n---\n%s", want, out)
		}
	}
	// Must stop at the terminator — the MOVE to NR$12 after it must not appear.
	if strings.Contains(out, "NR$12") {
		t.Errorf("disasm continued past the end-of-list WAIT (rendered NR$12):\n%s", out)
	}
	if strings.Contains(out, "HALT") {
		t.Errorf("disasm still names a HALT opcode the hardware does not have:\n%s", out)
	}
}
