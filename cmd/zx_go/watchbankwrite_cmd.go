package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// poolWriteWatch is one parsed --watch-bank-write entry: writes to
// 16K RAM bank `bank`, at byte offset in [lo,hi] inclusive, are
// logged with the source PC. Unlike --watch-writes (which keys on the
// CPU address and so conflates all banks mapped there over time), this
// keys on the unified-pool location, so "is bank 111's struct ever
// written?" gets a definitive answer regardless of paging.
type poolWriteWatch struct {
	bank   int
	lo, hi uint16
}

// parsePoolWriteSpec decodes "BANK:LO-HI[,BANK:LO-HI…]" (bank decimal,
// offsets hex with optional $/0x). HI optional (defaults to LO).
func parsePoolWriteSpec(spec string) []poolWriteWatch {
	var out []poolWriteWatch
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bankStr, rng, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		bank, err := strconv.Atoi(strings.TrimSpace(bankStr))
		if err != nil {
			continue
		}
		loStr, hiStr, hasHi := strings.Cut(rng, "-")
		lo, err := parseHexU16(loStr)
		if err != nil {
			continue
		}
		hi := lo
		if hasHi {
			if h, err := parseHexU16(hiStr); err == nil {
				hi = h
			}
		}
		out = append(out, poolWriteWatch{bank: bank, lo: lo, hi: hi})
	}
	return out
}

func parseHexU16(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "$")
	v, err := strconv.ParseUint(s, 16, 16)
	return uint16(v), err
}

// installBankWriteWatchers chains a ramWriteHook that logs every guest
// write landing in a watched (bank, offset-range), with the CPU PC.
func installBankWriteWatchers(mem *memory.Memory, cpu *z80.CPU, spec string) {
	watches := parsePoolWriteSpec(spec)
	if len(watches) == 0 {
		return
	}
	prior := mem.GetRAMWriteHook()
	mem.SetRAMWriteHook(func(bank int, off uint16, val byte) {
		if prior != nil {
			prior(bank, off, val)
		}
		for _, w := range watches {
			if bank == w.bank && off >= w.lo && off <= w.hi {
				slog.Debug("bank-write",
					"bank16k", bank,
					"off", fmt.Sprintf("$%04X", off),
					"val", fmt.Sprintf("$%02X", val),
					"pc", fmt.Sprintf("$%04X", cpu.PC),
				)
			}
		}
	})
}
