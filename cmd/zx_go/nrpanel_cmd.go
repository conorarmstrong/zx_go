package main

import (
	"fmt"
	"strings"
)

// formatNextRegPanel renders a decoded dashboard of the most useful
// NextRegs, reading each via rd. Pure function (reader injected) so it
// unit-tests without an emulator. jnext's GUI has a NextReg panel; this
// is the scriptable, decoded equivalent.
func formatNextRegPanel(rd func(byte) byte) string {
	var b strings.Builder
	b.WriteString("OK NextReg panel\r\n")

	r03 := rd(0x03)
	fmt.Fprintf(&b, "  $03 machine/timing  = $%02X  (config_mode=%d)\r\n",
		r03, b2u(r03&0x80 != 0))

	r07 := rd(0x07)
	speeds := [4]string{"3.5MHz", "7MHz", "14MHz", "28MHz"}
	fmt.Fprintf(&b, "  $07 CPU speed       = $%02X  (%s)\r\n",
		r07, speeds[r07&0x03])

	fmt.Fprintf(&b, "  $05 peripheral 1    = $%02X\r\n", rd(0x05))
	// Bit 4 here is only the DRIVE-button divMMC-NMI enable, not the
	// divMMC automap gate itself (that's NR$0A bit 4, decoded below).
	fmt.Fprintf(&b, "  $06 peripheral 2    = $%02X  (drive-nmi en=%d)\r\n",
		rd(0x06), b2u(rd(0x06)&0x10 != 0))
	fmt.Fprintf(&b, "  $8C altrom          = $%02X\r\n", rd(0x8C))
	fmt.Fprintf(&b, "  $8E paging          = $%02X\r\n", rd(0x8E))
	fmt.Fprintf(&b, "  $0A divMMC/automap  = $%02X  (automap en=%d)\r\n",
		rd(0x0A), b2u(rd(0x0A)&0x10 != 0))

	// MMU8 slot table ($50-$57): which 8K bank backs each 8K CPU slot.
	b.WriteString("  MMU8 slots $50-$57 :")
	for r := byte(0x50); r <= 0x57; r++ {
		fmt.Fprintf(&b, " %02X", rd(r))
	}
	b.WriteString("\r\n")

	// Layer 2 + sprite control.
	fmt.Fprintf(&b, "  $12 L2 bank=$%02X  $13 L2 shadow=$%02X  $70 L2 ctrl=$%02X\r\n",
		rd(0x12), rd(0x13), rd(0x70))
	fmt.Fprintf(&b, "  $34 sprite-select  = $%02X  $43 palette-ctrl=$%02X\r\n",
		rd(0x34), rd(0x43))
	return b.String()
}

func b2u(v bool) int {
	if v {
		return 1
	}
	return 0
}

// cmdNRPanel is the `nr-panel` debugger command — a decoded snapshot of
// the live NextReg file.
func (d *remoteDebugger) cmdNRPanel() string {
	if d.emu == nil || d.emu.nextRegs == nil {
		return "ERR nr-panel: no NextReg device (not a Next machine?)"
	}
	return formatNextRegPanel(d.emu.nextRegs.Raw)
}
