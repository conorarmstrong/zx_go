package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/ula"
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
// Copper. Rendering stops at the first WAIT that can never release (the
// program terminator) so trailing NOOPs aren't dumped, or after count
// instructions. Pure function (instruction reader injected) for unit testing.
//
// reachableLines is the running machine's frame height in scanlines. Lines are
// numbered from zero, so the raster reaches at most one below it and a WAIT for
// that line or any line above can never release. It has to be the machine's own
// figure and not the largest across models: 311 on the 128K timing the Next
// uses and 312 on the 48K, so a threshold of 312 would miss the Next's own
// terminator, and the Next is the machine the Copper exists on.
func formatCopperDisasm(ins func(uint16) copper.Instruction, cursor uint16, mode copper.StartMode, count, reachableLines int) string {
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
			fmt.Fprintf(&b, "WAIT  line=%d hpos=%d", in.Y, in.X)
			// A WAIT for a line the raster never reaches is how a list ends.
			// $FFFF decodes to WAIT line=511 hpos=63, and vcount never gets
			// there, so the copper parks on it. The hardware has no HALT
			// opcode to render instead (device/copper.vhd), and rendering one
			// implied a stop the silicon does not perform: in mode 3 the list
			// restarts at every VBL and runs again.
			if int(in.Y) >= reachableLines {
				b.WriteString("   ; parks here (end of list)\r\n")
				return b.String()
			}
			b.WriteString("\r\n")
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
	return formatCopperDisasm(c.Instruction, c.Cursor(), c.Mode(), copper.MaxInstructions,
		ula.LinesPerFrameFor(d.emu.mem.GetCurrentModel()))
}
