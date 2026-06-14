package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/copper"
)

func copperModeName(m copper.StartMode) string {
	switch m {
	case copper.StartStop:
		return "Stop"
	case copper.StartFromZero:
		return "FromZero"
	case copper.StartContinue:
		return "Continue"
	case copper.StartOnVBL:
		return "OnVBL"
	default:
		return "?"
	}
}

// formatCopperDisasm disassembles the Copper program. ins(i) returns
// the decoded instruction at index i; cursor/mode come from the live
// Copper. Rendering stops at the first HALT (the program terminator)
// so trailing NOOPs aren't dumped, or after count instructions. Pure
// function (instruction reader injected) for unit testing.
func formatCopperDisasm(ins func(uint16) copper.Instruction, cursor uint16, mode copper.StartMode, count int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "OK Copper  cursor=$%03X  mode=%s\r\n", cursor, copperModeName(mode))
	if count > copper.MaxInstructions {
		count = copper.MaxInstructions
	}
	for i := 0; i < count; i++ {
		in := ins(uint16(i))
		fmt.Fprintf(&b, "  %03X: ", i)
		switch in.Op {
		case copper.OpMOVE:
			fmt.Fprintf(&b, "MOVE  NR$%02X,$%02X\r\n", in.Reg, in.Val)
		case copper.OpWAIT:
			fmt.Fprintf(&b, "WAIT  line=%d hpos=%d\r\n", in.Y, in.X)
		case copper.OpHALT:
			b.WriteString("HALT\r\n")
			return b.String()
		default:
			b.WriteString("NOOP\r\n")
		}
	}
	return b.String()
}

// cmdCopperDisasm is the `copper-disasm` debugger command. Reads the
// live Copper program and renders it.
func (d *remoteDebugger) cmdCopperDisasm() string {
	if d.emu == nil || d.emu.nextCopper == nil {
		return "ERR copper-disasm: no Copper device (not a Next machine?)"
	}
	c := d.emu.nextCopper
	return formatCopperDisasm(c.Instruction, c.Cursor(), c.Mode(), copper.MaxInstructions)
}
