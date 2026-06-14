package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// formatPaletteDump renders the first `count` entries of a palette as
// a 9-bit-hex grid (16 per row, each row labelled with its starting
// index) followed by the decoded 8-bit RGB of entry 0 as a sample.
// Pure function (takes the palette directly) for unit testing.
func formatPaletteDump(p *palette.Palette, count int) string {
	if p == nil {
		return "ERR palette-dump: nil palette\r\n"
	}
	if count > 256 {
		count = 256
	}
	var b strings.Builder
	b.WriteString("OK Palette (9-bit RRRGGGBBb)\r\n")
	for i := 0; i < count; i += 16 {
		fmt.Fprintf(&b, "  $%02X:", i)
		for j := 0; j < 16 && i+j < count; j++ {
			fmt.Fprintf(&b, " %03X", p.Get(byte(i+j)))
		}
		b.WriteString("\r\n")
	}
	r, g, b8 := p.RGB(0)
	fmt.Fprintf(&b, "  entry 0 RGB = #%02X%02X%02X\r\n", r, g, b8)
	return b.String()
}

// cmdPaletteDump is the `palette-dump` debugger command — a decoded
// snapshot of the live active palette.
func (d *remoteDebugger) cmdPaletteDump() string {
	if d.emu == nil || d.emu.nextPalette == nil {
		return "ERR palette-dump: no palette bank (not a Next machine?)"
	}
	return formatPaletteDump(d.emu.nextPalette.Active(), 256)
}
