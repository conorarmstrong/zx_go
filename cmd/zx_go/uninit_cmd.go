package main

import (
	"fmt"
	"log/slog"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// installUninitDetector wires the unified-RAM uninitialised-read
// detector (memory.EnableUninitTracking) into a headless run. When a
// guest reads a RAM byte it never wrote since power-on, the observer
// logs the source PC + pool location. Output is deduped by (pc,addr)
// and capped so a busy boot doesn't drown the log; an optional PC
// window (lo,hi inclusive; hi==0 = no upper bound) restricts logging
// to a code region of interest (e.g. the NextZXOS SD driver).
//
// Answers the cold-boot question: is a garbage pointer/value sourced
// from uninitialised RAM (a missing init step) or computed from
// written bytes (a logic bug)? Reference-free. See DEBUGGER.md.
func installUninitDetector(mem *memory.Memory, cpu *z80.CPU, pcLo, pcHi uint16, cap int) {
	if cap <= 0 {
		cap = 200
	}
	mem.EnableUninitTracking()
	type key struct {
		pc   uint16
		addr uint16
	}
	seen := map[key]bool{}
	count := 0
	mem.UninitReadObserver = func(addr uint16, bank16k int, off uint16) {
		if count >= cap {
			return
		}
		pc := cpu.PC
		if pcLo != 0 || pcHi != 0 {
			if pc < pcLo || (pcHi != 0 && pc > pcHi) {
				return
			}
		}
		k := key{pc, addr}
		if seen[k] {
			return
		}
		seen[k] = true
		count++
		slog.Debug("uninit-read",
			"pc", fmt.Sprintf("$%04X", pc),
			"addr", fmt.Sprintf("$%04X", addr),
			"bank16k", bank16k,
			"off", fmt.Sprintf("$%04X", off),
		)
		if count == cap {
			slog.Debug("uninit-read", "note", "cap reached; further uninit reads suppressed")
		}
	}
}
