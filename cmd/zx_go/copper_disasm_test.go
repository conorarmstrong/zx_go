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
		{Op: copper.OpWAIT, Y: 511, X: 63},        // $FFFF: the end-of-list terminator
		{Op: copper.OpMOVE, Reg: 0x12, Val: 0xFF}, // past it: must NOT render
	}
	rd := func(i uint16) copper.Instruction {
		if int(i) < len(prog) {
			return prog[i]
		}
		return copper.Instruction{Op: copper.OpNOOP}
	}

	out := formatCopperDisasm(rd, 0x00A, copper.StartOnVBL, 64, 312)

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
	// Must stop at the terminator: the MOVE to NR$12 after it must not appear.
	if strings.Contains(out, "NR$12") {
		t.Errorf("disasm continued past the end-of-list WAIT (rendered NR$12):\n%s", out)
	}
	if strings.Contains(out, "HALT") {
		t.Errorf("disasm still names a HALT opcode the hardware does not have:\n%s", out)
	}
}

// $FFFF is the idiomatic terminator, but it is not the only WAIT that parks.
// Any target line the raster cannot reach parks, and the raster reaches at most
// c_max_vc: 311 on a 312-line 48K frame, 310 on the 311-line 128K family
// (video/zxula_timing.vhd). So line 312 parks exactly as surely as line 511,
// and the disassembler has to stop at it rather than print the trailing NOOPs
// the terminator exists to suppress.
//
// This is a boundary the 511 case cannot cover: copperMaxFrameLine holds the
// frame's line COUNT, and comparing it as a maximum line NUMBER lets the first
// unreachable line through.
func TestDisasmStopsAtTheLowestUnreachableWaitLine(t *testing.T) {
	prog := []copper.Instruction{
		{Op: copper.OpMOVE, Reg: 0x40, Val: 0x01},
		{Op: copper.OpWAIT, Y: 312, X: 0}, // the lowest line no raster reaches
		{Op: copper.OpMOVE, Reg: 0x41, Val: 0x02},
		{Op: copper.OpMOVE, Reg: 0x42, Val: 0x03},
	}
	ins := func(i uint16) copper.Instruction {
		if int(i) < len(prog) {
			return prog[i]
		}
		return copper.Instruction{Op: copper.OpNOOP}
	}
	out := formatCopperDisasm(ins, 0, copper.StartFromZero, 64, 312)

	if !strings.Contains(out, "parks") {
		t.Errorf("WAIT line=312 was not reported as parking:\n%s", out)
	}
	if strings.Contains(out, "$41") || strings.Contains(out, "$42") {
		t.Errorf("disassembly continued past a WAIT that can never release:\n%s", out)
	}
}

// The terminator threshold has to follow the running machine, not the largest
// frame across models. A 128K-timing frame is 311 lines, numbered 0..310, so a
// WAIT for line 311 parks on it; a 48K frame is 312 lines and 311 is an
// ordinary reachable line there. Using the maximum across models made the
// disassembler blind to the Next's own terminator, which is the machine the
// Copper exists on.
func TestDisasmTerminatorThresholdFollowsTheMachine(t *testing.T) {
	prog := []copper.Instruction{
		{Op: copper.OpWAIT, Y: 311, X: 0},
		{Op: copper.OpMOVE, Reg: 0x41, Val: 0x02},
	}
	ins := func(i uint16) copper.Instruction {
		if int(i) < len(prog) {
			return prog[i]
		}
		return copper.Instruction{Op: copper.OpNOOP}
	}

	next := formatCopperDisasm(ins, 0, copper.StartFromZero, 64, 311)
	if !strings.Contains(next, "parks") {
		t.Errorf("on 311-line timing, WAIT line=311 must park:\n%s", next)
	}
	if strings.Contains(next, "$41") {
		t.Errorf("on 311-line timing, disassembly continued past a parking WAIT:\n%s", next)
	}

	fortyEight := formatCopperDisasm(ins, 0, copper.StartFromZero, 64, 312)
	if strings.Contains(fortyEight, "parks") {
		t.Errorf("on 312-line timing, line 311 is reachable and must not be "+
			"reported as parking:\n%s", fortyEight)
	}
}
