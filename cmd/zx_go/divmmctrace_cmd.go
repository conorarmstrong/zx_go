package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
)

// divMMCTraceSpec is the active filter for trace-divmmc-ram. Stored
// behind an atomic.Pointer so the hot-path writeLogger reads it
// without locking; mutations come from the (rare) ZRCP command.
type divMMCTraceSpec struct {
	bank  int    // -1 = any bank
	loOff uint16 // inclusive lower offset within the bank ($0000-$1FFF)
	hiOff uint16 // inclusive upper offset
}

// divMMCTraceState is per-debugger state for trace-divmmc-ram. The
// activeSpec drives whether the writeLogger emits; a count is
// maintained for the user-visible "trace-divmmc-ram count" reply.
type divMMCTraceState struct {
	spec      atomic.Pointer[divMMCTraceSpec]
	count     atomic.Uint64
	installed bool
}

// cmdTraceDivMMCRAM is the runtime trace tap. Forms:
//
//	trace-divmmc-ram                 — show current filter and hit count
//	trace-divmmc-ram off             — disable the trace
//	trace-divmmc-ram all             — log every divMMC RAM write
//	trace-divmmc-ram BANK            — only bank N (0..15)
//	trace-divmmc-ram BANK OFF        — only bank N at exactly OFF
//	trace-divmmc-ram BANK LO-HI      — bank N, offset window LO..HI
//	trace-divmmc-ram any LO-HI       — any bank, offset window LO..HI
//
// All hex values accept `$XXXX`, `0xXXXX`, or bare hex. Offsets are
// within the 8 KB bank ($0000-$1FFF). Output goes to slog.Info with
// fields bank/addr/val/pc/insns — one event per write.
//
// NOTE: this REPLACES any previously-installed writeLogger. The
// existing env-var diagnostics (`ZX_GO_DIVMMC_WRITE_TRACE`,
// `ZX_GO_PORT_E3_DEEP_TRACE`) use the same single-logger slot; if
// they were set at startup, this command clobbers them. Toggle off
// then restart the process to restore env-var behaviour.
func (d *remoteDebugger) cmdTraceDivMMCRAM(args []string) string {
	pager, ok := d.emu.ula.NextDivMMC().(*divmmc.Pager)
	if !ok || pager == nil {
		return "ERR divMMC pager not wired (Spectrum Next mode only)"
	}
	// No args: report state.
	if len(args) == 0 {
		specP := d.divMMCTrace.spec.Load()
		if specP == nil {
			return "OK trace-divmmc-ram off"
		}
		s := *specP
		return fmt.Sprintf("OK trace-divmmc-ram bank=%s offsets=$%04X-$%04X hits=%d",
			fmtBank(s.bank), s.loOff, s.hiOff, d.divMMCTrace.count.Load())
	}
	// off / clear
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.divMMCTrace.spec.Store(nil)
		d.divMMCTrace.count.Store(0)
		pager.SetWriteLogger(nil)
		d.divMMCTrace.installed = false
		return "OK trace-divmmc-ram cleared"
	}
	// Parse filter: BANK [OFF | LO-HI] OR "all" / "any" forms.
	spec := divMMCTraceSpec{bank: -1, loOff: 0, hiOff: 0x1FFF}
	switch strings.ToLower(args[0]) {
	case "all":
		// all banks, all offsets
	case "any":
		// "any LO-HI"
		if len(args) >= 2 {
			lo, hi, err := parseOffsetRange(args[1])
			if err != nil {
				return "ERR " + err.Error()
			}
			spec.loOff, spec.hiOff = lo, hi
		}
	default:
		b, err := strconv.Atoi(args[0])
		if err != nil || b < 0 || b > 15 {
			return "ERR bank must be 0..15, `any`, or `all`"
		}
		spec.bank = b
		if len(args) >= 2 {
			lo, hi, err := parseOffsetRange(args[1])
			if err != nil {
				return "ERR " + err.Error()
			}
			spec.loOff, spec.hiOff = lo, hi
		}
	}
	d.divMMCTrace.spec.Store(&spec)
	d.divMMCTrace.count.Store(0)
	// Install (or refresh) the writeLogger. The closure reads the
	// atomic pointer each fire so filter updates take effect
	// immediately without re-installing.
	if !d.divMMCTrace.installed {
		pager.SetWriteLogger(func(bank int, addr uint16, val byte) {
			specP := d.divMMCTrace.spec.Load()
			if specP == nil {
				return
			}
			s := *specP
			if s.bank >= 0 && s.bank != bank {
				return
			}
			off := addr & 0x1FFF
			if off < s.loOff || off > s.hiOff {
				return
			}
			d.divMMCTrace.count.Add(1)
			c := d.emu.cpu
			slog.Info("divmmc-ram-trace",
				"bank", bank,
				"addr", fmt.Sprintf("$%04X", addr),
				"val", fmt.Sprintf("$%02X", val),
				"pc", fmt.Sprintf("$%04X", c.PC),
				"insns", c.InstructionCount())
		})
		d.divMMCTrace.installed = true
	}
	return fmt.Sprintf("OK trace-divmmc-ram bank=%s offsets=$%04X-$%04X",
		fmtBank(spec.bank), spec.loOff, spec.hiOff)
}

func fmtBank(b int) string {
	if b < 0 {
		return "any"
	}
	return strconv.Itoa(b)
}

// parseOffsetRange accepts "LO-HI" (both hex) or single-value
// "X" (which sets LO=HI=X).
func parseOffsetRange(s string) (uint16, uint16, error) {
	if dash := strings.Index(s, "-"); dash >= 0 {
		lo, err := parseHex(s[:dash])
		if err != nil {
			return 0, 0, fmt.Errorf("bad LO: %w", err)
		}
		hi, err := parseHex(s[dash+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("bad HI: %w", err)
		}
		if lo > hi {
			return 0, 0, fmt.Errorf("LO > HI")
		}
		return lo & 0x1FFF, hi & 0x1FFF, nil
	}
	v, err := parseHex(s)
	if err != nil {
		return 0, 0, err
	}
	return v & 0x1FFF, v & 0x1FFF, nil
}
