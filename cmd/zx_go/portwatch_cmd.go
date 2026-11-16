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
	mask     uint16 // 0: the default rule (low byte below $100, else exact)
	matchVal *byte
	logOnly  bool
	dir      watchDir
}

// watchDir is which accesses a port or NextReg watch fires on.
type watchDir byte

const (
	watchWrite watchDir = iota
	watchRead
	watchBoth
)

func (w watchDir) fires(write bool) bool {
	return w == watchBoth || (w == watchWrite) == write
}

func (w watchDir) String() string {
	return [...]string{"write", "read", "read+write"}[w]
}

// parseWatchDir strips a trailing read / rw / write keyword.
func parseWatchDir(args []string) ([]string, watchDir) {
	if len(args) == 0 {
		return args, watchWrite
	}
	switch strings.ToLower(args[len(args)-1]) {
	case "read":
		return args[:len(args)-1], watchRead
	case "rw":
		return args[:len(args)-1], watchBoth
	case "write":
		return args[:len(args)-1], watchWrite
	}
	return args, watchWrite
}

// parseWatchKeywords strips the trailing direction and log keywords, in
// either order.
func parseWatchKeywords(args []string) (rest []string, dir watchDir, logOnly bool) {
	dir = watchWrite
	for len(args) > 0 {
		last := strings.ToLower(args[len(args)-1])
		if last == "log" {
			logOnly = true
			args = args[:len(args)-1]
			continue
		}
		shorter, d := parseWatchDir(args)
		if len(shorter) == len(args) {
			break
		}
		args, dir = shorter, d
	}
	return args, dir, logOnly
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
func (s *portWatchSet) match(port uint16, val byte, write bool) (matched, halt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.watches {
		if !w.dir.fires(write) {
			continue
		}
		if w.mask != 0 {
			if port&w.mask != w.port&w.mask {
				continue
			}
		} else if w.port < 0x100 {
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

// cmdWatchPort arms a port watch.
//
//	watch-port PORT              fire on any write to PORT (halts CPU)
//	watch-port PORT/MASK         compare only the address bits in MASK
//	watch-port PORT =VAL         halt only when the byte equals VAL
//	watch-port PORT read | rw    fire on reads, or on reads and writes
//	watch-port PORT log          log every access to PORT, do NOT halt
//	watch-port off               clear all
//
// The keywords can be combined, in either order: `watch-port $FE
// read log`.
func (d *remoteDebugger) cmdWatchPort(args []string) string {
	if len(args) >= 1 && strings.ToLower(args[0]) == "off" {
		d.portWatches.clear()
		return "OK port watches cleared"
	}
	args, dir, logOnly := parseWatchKeywords(args)
	if (logOnly || dir != watchWrite) && len(args) == 0 {
		return "ERR watch-port: no port given"
	}
	if len(args) < 1 {
		ws := d.portWatches.list()
		if len(ws) == 0 {
			return "OK (no port watches)"
		}
		var b strings.Builder
		b.WriteString("OK\r\n")
		for _, w := range ws {
			port := fmt.Sprintf("$%04X", w.port)
			if w.mask != 0 {
				port += fmt.Sprintf("/$%04X", w.mask)
			}
			if w.matchVal != nil {
				fmt.Fprintf(&b, "  port=%s val=$%02X (%s)\r\n", port, *w.matchVal, w.dir)
			} else {
				fmt.Fprintf(&b, "  port=%s (any, %s)\r\n", port, w.dir)
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
	var mask uint16
	if i := strings.Index(portStr, "/"); i >= 0 {
		m, err := parseHex(strings.TrimSpace(portStr[i+1:]))
		if err != nil || m == 0 {
			return "ERR bad mask: " + portStr[i+1:]
		}
		mask = m
		portStr = strings.TrimSpace(portStr[:i])
	}
	port, err := parseHex(portStr)
	if err != nil {
		return "ERR bad port: " + err.Error()
	}
	spec := portWatchSpec{port: port, mask: mask, logOnly: logOnly, dir: dir}
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
	what := fmt.Sprintf("$%04X", spec.port)
	if mask != 0 {
		what += fmt.Sprintf("/$%04X", mask)
	}
	if spec.matchVal != nil {
		return fmt.Sprintf("OK watching port %s for val=$%02X (%s, %s)", what, *spec.matchVal, dir, mode)
	}
	return fmt.Sprintf("OK watching port %s (any value, %s, %s)", what, dir, mode)
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
		matched, halt := d.portWatches.match(addr, val, write)
		if !matched {
			return
		}
		slog.Info("watch-port hit",
			"port", fmt.Sprintf("$%04X", addr),
			"write", write,
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
