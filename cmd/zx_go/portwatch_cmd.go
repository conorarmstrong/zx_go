package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// portWatchSpec is one armed port-write watch. matchVal == nil
// means "any value"; otherwise the watch fires only when the
// guest's OUT byte equals *matchVal. If logOnly is true the watch
// emits an slog.Info line but does NOT halt the CPU — handy for
// observing tape-loader / border-modulation patterns over many
// frames without freezing on the first write.
type portWatchSpec struct {
	port     uint16
	matchVal *byte
	logOnly  bool
}

// portWatchSet protects the live watch list. A telnet command
// mutates it under the mutex; the tracer reads it on every port
// write — guarded by mu so the tracer's iteration is consistent.
type portWatchSet struct {
	mu      sync.Mutex
	watches []portWatchSpec
}

func (s *portWatchSet) add(spec portWatchSpec) {
	s.mu.Lock()
	s.watches = append(s.watches, spec)
	s.mu.Unlock()
}

func (s *portWatchSet) clear() {
	s.mu.Lock()
	s.watches = nil
	s.mu.Unlock()
}

// match returns true if the (port, val) write satisfies any of the
// active watches. Called from the ULA port tracer on every write.
// Ports below $100 are matched against the low byte of the actual
// port address — `OUT ($FE),A` writes go to $xxFE (high byte is A),
// so a watch on $FE catches every border write regardless of A.
// Ports >= $100 require exact match (e.g. $243B / $253B for NextReg
// port pair).
func (s *portWatchSet) match(port uint16, val byte) (matched, halt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.watches {
		if w.port < 0x100 {
			if byte(w.port) != byte(port) {
				continue
			}
		} else if w.port != port {
			continue
		}
		if w.matchVal == nil || *w.matchVal == val {
			matched = true
			if !w.logOnly {
				halt = true
				return
			}
		}
	}
	return
}

func (s *portWatchSet) list() []portWatchSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]portWatchSpec, len(s.watches))
	copy(out, s.watches)
	return out
}

// cmdWatchPort arms a port-write watch.
//
//	watch-port PORT          fire on any write to PORT (halts CPU)
//	watch-port PORT =VAL     halt only when the byte equals VAL
//	watch-port PORT log       log every write to PORT, do NOT halt
//	watch-port off           clear all
func (d *remoteDebugger) cmdWatchPort(args []string) string {
	if len(args) >= 1 && strings.ToLower(args[0]) == "off" {
		d.portWatches.clear()
		return "OK port watches cleared"
	}
	logOnly := false
	if len(args) >= 2 && strings.ToLower(args[len(args)-1]) == "log" {
		logOnly = true
		args = args[:len(args)-1]
	}
	if len(args) < 1 {
		ws := d.portWatches.list()
		if len(ws) == 0 {
			return "OK (no port watches)"
		}
		var b strings.Builder
		b.WriteString("OK\r\n")
		for _, w := range ws {
			if w.matchVal != nil {
				fmt.Fprintf(&b, "  port=$%04X val=$%02X\r\n", w.port, *w.matchVal)
			} else {
				fmt.Fprintf(&b, "  port=$%04X (any)\r\n", w.port)
			}
		}
		return strings.TrimRight(b.String(), "\r\n")
	}
	// Parse PORT [=VAL] — "=" may be glued onto either side.
	raw := strings.Join(args, " ")
	raw = strings.TrimSpace(raw)
	var portStr, valStr string
	if i := strings.Index(raw, "="); i >= 0 {
		portStr = strings.TrimSpace(raw[:i])
		valStr = strings.TrimSpace(raw[i+1:])
	} else {
		portStr = raw
	}
	port, err := parseHex(portStr)
	if err != nil {
		return "ERR bad port: " + err.Error()
	}
	spec := portWatchSpec{port: port, logOnly: logOnly}
	if valStr != "" {
		v, err := parseHex(valStr)
		if err != nil {
			return "ERR bad val: " + err.Error()
		}
		b := byte(v & 0xFF)
		spec.matchVal = &b
	}
	d.portWatches.add(spec)
	d.ensurePortWatchHook()
	mode := "halt"
	if logOnly {
		mode = "log-only"
	}
	if spec.matchVal != nil {
		return fmt.Sprintf("OK watching port $%04X for val=$%02X (%s)", spec.port, *spec.matchVal, mode)
	}
	return fmt.Sprintf("OK watching port $%04X (any value, %s)", spec.port, mode)
}

// ensurePortWatchHook chains a tracer onto the ULA on the first
// watch-port invocation. The chain calls any previously-installed
// tracer first, then evaluates the watch set and halts the CPU on
// a match.
func (d *remoteDebugger) ensurePortWatchHook() {
	if d.portWatchInstalled {
		return
	}
	d.portWatchInstalled = true
	prior := d.emu.ula.GetPortTracer()
	d.emu.ula.SetPortTracer(func(addr uint16, val byte, write, handled bool) {
		if prior != nil {
			prior(addr, val, write, handled)
		}
		if !write {
			return
		}
		matched, halt := d.portWatches.match(addr, val)
		if !matched {
			return
		}
		slog.Info("watch-port hit",
			"port", fmt.Sprintf("$%04X", addr),
			"val", fmt.Sprintf("$%02X", val),
			"pc", fmt.Sprintf("$%04X", d.emu.cpu.PC))
		if halt {
			d.paused.Store(true)
			d.snapshotOnBPHit(d.emu.cpu.PC, "watch-port")
		}
	})
}

// Compile-time interface check — we depend only on the bit of ULA
// we use. Keeps the dependency surface explicit.
var _ ula.PortTracer = ula.PortTracer(nil)
