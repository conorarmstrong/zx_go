package main

import (
	"fmt"
	"os"
	"strings"
)

// cmdLoadBin implements `load-bin KIND BANK OFF FILE [LEN]` — reads
// raw bytes from the host filesystem and pokes them into a specific
// emulator-side bank at a specific offset. Complement to `bank-poke`
// (which accepts inline hex bytes); use `load-bin` when the payload
// is too large for a command line — 16 KB ROM banks, 64 KB snapshots,
// or per-bank dumps captured from another emulator.
//
// KIND values: ram | rom | altrom | divmmc-ram (same vocabulary as
// `bank-peek` / `bank-poke`).
//
// The file is read in its entirety (or up to LEN bytes if LEN is
// supplied) and written sequentially starting at the target bank's
// OFF. Writes use the same dispatch as `bank-poke`, so MAPRAM /
// stub-protected windows still drop bytes.
//
// Typical use: capture state from another emulator (e.g. a reference emulator
// `save-binary` dump of a runtime bank), then `load-bin ram 0 0 file`
// to overlay that bank as a starting point and ask "does our boot
// progress correctly from this state?"
//
// Examples:
//
//	load-bin ram 0 0 /tmp/zes_bank0.bin
//	load-bin rom 2 0 /tmp/patched_rom2.bin
//	load-bin divmmc-ram 1 $1F00 /tmp/trampoline.bin $100
func (d *remoteDebugger) cmdLoadBin(args []string) string {
	if len(args) < 4 {
		return "ERR usage: load-bin KIND BANK OFF FILE [LEN]  (kinds: ram rom altrom divmmc-ram)"
	}
	kind := strings.ToLower(args[0])
	bank, err := parseHex(args[1])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	off, err := parseHex(args[2])
	if err != nil {
		return "ERR bad off: " + err.Error()
	}
	path := args[3]
	data, err := os.ReadFile(path)
	if err != nil {
		return "ERR read " + path + ": " + err.Error()
	}
	if len(args) >= 5 {
		limit, err := parseHex(args[4])
		if err != nil {
			return "ERR bad len: " + err.Error()
		}
		if int(limit) < len(data) {
			data = data[:limit]
		}
	}
	if perr := d.banks().BankPoke(kind, int(bank), int(off), data); perr != nil {
		return "ERR " + perr.Error()
	}
	return fmt.Sprintf("OK loaded %d bytes from %s into %s bank=%d off=$%04X",
		len(data), path, kind, bank, off)
}
