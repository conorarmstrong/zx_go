package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// nrPanelRegs is the curated dashboard: machine and speed, peripherals, paging,
// video, and the interrupt block.
var nrPanelRegs = []byte{
	0x03, 0x05, 0x06, 0x07, 0x08, 0x0A, 0x0B, 0x8C, 0x8E,
	0x12, 0x13, 0x15, 0x22, 0x43, 0x68, 0x69, 0x6B, 0x70,
	0xC0, 0xC4, 0xC5, 0xC6, 0xCC, 0xCD, 0xCE,
}

// formatNRLine renders one register: number, name, value and decoded fields.
func formatNRLine(r, v byte) string {
	line := fmt.Sprintf("  $%02X %-28s = $%02X", r, nextregs.Name(r), v)
	if dec := nextregs.Decode(r, v); dec != "" {
		line += "  " + dec
	}
	return line
}

// formatNextRegPanel renders a decoded dashboard of the most useful NextRegs,
// reading each via rd. Pure function (reader injected) so it unit-tests
// without an emulator.
func formatNextRegPanel(rd func(byte) byte) string {
	var b strings.Builder
	b.WriteString("OK NextReg panel\r\n")
	for _, r := range nrPanelRegs {
		b.WriteString(formatNRLine(r, rd(r)) + "\r\n")
	}
	// MMU8 slot table ($50-$57): which 8K bank backs each 8K CPU slot.
	b.WriteString("  MMU8 slots $50-$57 :")
	for r := byte(0x50); r <= 0x57; r++ {
		fmt.Fprintf(&b, " %02X", rd(r))
	}
	b.WriteString("\r\n")
	return b.String()
}

// nextRegListLines renders every named register, or the ones given.
func nextRegListLines(rd func(byte) byte, regs []byte) []string {
	if regs == nil {
		for r := 0; r < 256; r++ {
			if nextregs.Name(byte(r)) != "" {
				regs = append(regs, byte(r))
			}
		}
	}
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = formatNRLine(r, rd(r))
	}
	return out
}

// formatNextRegList is nextRegListLines as a command reply.
func formatNextRegList(rd func(byte) byte, regs []byte) string {
	return "OK\r\n" + strings.Join(nextRegListLines(rd, regs), "\r\n")
}

// cmdNRPanel is the `nr-panel` debugger command, a decoded snapshot of the
// live NextReg file. It reads through Peek: the values a guest would read,
// without tripping the tracer.
func (d *remoteDebugger) cmdNRPanel() string {
	if d.emu == nil || d.emu.nextRegs == nil {
		return "ERR nr-panel: no NextReg device (not a Next machine?)"
	}
	return formatNextRegPanel(d.emu.nextRegs.Peek)
}

// cmdNRList is `nr-list [REG…]`: every named register decoded, or the ones
// listed.
func (d *remoteDebugger) cmdNRList(args []string) string {
	if d.emu == nil || d.emu.nextRegs == nil {
		return "ERR nr-list: no NextReg device (not a Next machine?)"
	}
	var regs []byte
	for _, a := range args {
		v, err := parseHex(a)
		if err != nil || v > 0xFF {
			return "ERR nr-list: bad register " + a
		}
		regs = append(regs, byte(v))
	}
	return formatNextRegList(d.emu.nextRegs.Peek, regs)
}

// hwLines joins a block's debug view as a command reply.
func hwLines(name string, lines []string) string {
	if lines == nil {
		return "ERR " + name + ": not present (not a Next machine?)"
	}
	return "OK\r\n  " + strings.Join(lines, "\r\n  ")
}

// cmdHardware answers im2, nmi, dma, uart and ctc.
func (d *remoteDebugger) cmdHardware(name string) string {
	e := d.emu
	var lines []string
	switch name {
	case "im2":
		if e.nextIM2 != nil {
			lines = e.nextIM2.DebugLines()
		}
	case "nmi":
		if e.nextReset != nil {
			lines = e.nextReset.NMIDebugLines()
		}
	case "dma":
		if e.nextDMA != nil {
			lines = e.nextDMA.DebugLines()
		}
	case "uart":
		if e.nextUART != nil {
			lines = e.nextUART.Block.DebugLines()
		}
	case "ctc":
		if e.nextCTC != nil {
			lines = e.nextCTC.DebugLines()
		}
	}
	return hwLines(name, lines)
}
