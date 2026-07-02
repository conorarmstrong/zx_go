package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// nrTraceSet is the runtime-mutable set of NextReg numbers to log
// on write. Distinct from any tracer installed via the
// ZX_GO_NEXTREG_WATCH env var — the runtime `nr-trace` command
// manages this set independently and the tracer is chained on top
// of whatever was previously installed.
type nrTraceSet struct {
	mu      sync.Mutex
	watched map[byte]bool
}

func (s *nrTraceSet) add(reg byte) {
	s.mu.Lock()
	if s.watched == nil {
		s.watched = map[byte]bool{}
	}
	s.watched[reg] = true
	s.mu.Unlock()
}

func (s *nrTraceSet) clear() {
	s.mu.Lock()
	s.watched = nil
	s.mu.Unlock()
}

func (s *nrTraceSet) match(reg byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watched[reg]
}

func (s *nrTraceSet) list() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, 0, len(s.watched))
	for r := range s.watched {
		out = append(out, r)
	}
	// Stable order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// cmdNRTrace adds NextReg numbers to the live trace set, lists the
// current set, or clears it.
//
//	nr-trace            list active
//	nr-trace REG[,REG…] add (hex)
//	nr-trace off        clear all
func (d *remoteDebugger) cmdNRTrace(args []string) string {
	if len(args) == 0 {
		regs := d.nrTraces.list()
		if len(regs) == 0 {
			return "OK (no nr-trace)"
		}
		parts := make([]string, len(regs))
		for i, r := range regs {
			parts[i] = fmt.Sprintf("$%02X", r)
		}
		return "OK nr-trace=" + strings.Join(parts, ",")
	}
	if strings.ToLower(args[0]) == "off" {
		d.nrTraces.clear()
		return "OK nr-trace cleared"
	}
	for _, p := range strings.Split(strings.Join(args, ","), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseHex(p)
		if err != nil {
			return "ERR bad reg " + p + ": " + err.Error()
		}
		d.nrTraces.add(byte(v & 0xFF))
	}
	d.ensureNRTraceHook()
	return "OK nr-trace=" + strings.Join(args, ",")
}

// ensureNRTraceHook installs the chained tracer on the first
// nr-trace add. Any pre-existing tracer (e.g. the env-var-driven
// startup tracer in next.go) continues to fire.
func (d *remoteDebugger) ensureNRTraceHook() {
	if d.nrTraceInstalled {
		return
	}
	d.nrTraceInstalled = true
	prior := d.emu.nextRegs.GetTracer()
	d.emu.nextRegs.SetTracer(func(reg, val byte, isWrite bool) {
		if prior != nil {
			prior(reg, val, isWrite)
		}
		if !isWrite {
			return
		}
		if !d.nrTraces.match(reg) {
			return
		}
		slog.Info("nr-trace",
			"reg", fmt.Sprintf("$%02X", reg),
			"val", fmt.Sprintf("$%02X", val),
			"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC))
	})
}

// compile-time check that the TraceFunc signature matches.
var _ nextregs.TraceFunc = nextregs.TraceFunc(nil)
