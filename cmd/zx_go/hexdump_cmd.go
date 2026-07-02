package main

import (
	"fmt"
	"strings"
)

// formatHexDump renders data as a classic hexdump: one row per 16 bytes,
// each row = "$ADDR  hh hh … hh  |ascii|". Non-printable bytes show as
// '.' in the gutter. Pure function for easy unit testing.
func formatHexDump(addr uint16, data []byte) string {
	var b strings.Builder
	for off := 0; off < len(data); off += 16 {
		end := off + 16
		if end > len(data) {
			end = len(data)
		}
		row := data[off:end]
		fmt.Fprintf(&b, "$%04X ", addr+uint16(off))
		for i := 0; i < 16; i++ {
			if i < len(row) {
				fmt.Fprintf(&b, " %02X", row[i])
			} else {
				b.WriteString("   ")
			}
		}
		b.WriteString("  ")
		for _, c := range row {
			if c >= 0x20 && c < 0x7F {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteString("\r\n")
	}
	return b.String()
}

// cmdHexDump implements the `hexdump` debugger command — a visual
// address/hex/ASCII dump (vs the flat byte list from `get-memory`).
func (d *remoteDebugger) cmdHexDump(args []string) string {
	if len(args) < 2 {
		return "ERR usage: hexdump ADDR LEN"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	n, err := parseHex(args[1])
	if err != nil {
		return "ERR " + err.Error()
	}
	if n > 1024 {
		n = 1024
	}
	data := make([]byte, n)
	for i := uint16(0); i < n; i++ {
		data[i] = d.emu.mem.Read(addr + i)
	}
	return strings.TrimRight("OK\r\n"+formatHexDump(addr, data), "\r\n")
}
