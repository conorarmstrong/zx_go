package main

import (
	"fmt"
	"strings"
)

// cmdSym handles three forms:
//
//	sym                   — list every loaded symbol, addr-sorted
//	sym $ADDR NAME ...    — add or replace the label for ADDR
//	sym clear $ADDR       — drop the label for ADDR
//
// Names may contain spaces; everything after the address is joined.
// Symbols added here survive only the running session; persist
// changes by editing the file `--symbol-map` was loaded from.
func (d *remoteDebugger) cmdSym(args []string) string {
	if len(args) == 0 {
		entries := listSymbols()
		if len(entries) == 0 {
			return "OK (no symbols loaded)"
		}
		var b strings.Builder
		b.WriteString("OK\r\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "  $%04X  %s\r\n", e.Addr, e.Name)
		}
		return strings.TrimRight(b.String(), "\r\n")
	}
	if strings.EqualFold(args[0], "clear") {
		if len(args) < 2 {
			return "ERR usage: sym clear $ADDR"
		}
		addr, err := parseHex(args[1])
		if err != nil {
			return "ERR " + err.Error()
		}
		clearSymbol(addr)
		return fmt.Sprintf("OK cleared $%04X", addr)
	}
	if len(args) < 2 {
		return "ERR usage: sym $ADDR NAME"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	name := strings.Join(args[1:], " ")
	addSymbol(addr, name)
	return fmt.Sprintf("OK $%04X = %s", addr, name)
}

// cmdReloadSyms re-reads the supplied symbol-map file, replacing
// the in-memory table. Useful after editing the file mid-session
// without restarting zx_go.
func (d *remoteDebugger) cmdReloadSyms(args []string) string {
	if len(args) < 1 {
		return "ERR usage: reload-syms PATH"
	}
	loadSymbolMap(args[0])
	n := len(listSymbols())
	return fmt.Sprintf("OK reloaded %d symbols from %s", n, args[0])
}
