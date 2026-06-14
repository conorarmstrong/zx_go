package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// cmdWatchReg implements `watch-reg REG [from VAL] [to VAL]`. The
// register name must be one the cpuState.Reg adapter knows about
// (pc, sp, a, f, b, c, d, e, h, l, ix, iy, iff1, iff2, im, halted,
// bank). VAL accepts hex ($XX / 0xXX) or decimal-with-d suffix.
func (d *remoteDebugger) cmdWatchReg(args []string) string {
	if len(args) < 1 {
		return "ERR usage: watch-reg REG [from VAL] [to VAL]"
	}
	reg := strings.ToLower(args[0])
	w := debugger.RegWatch{Reg: reg}
	i := 1
	for i < len(args) {
		switch strings.ToLower(args[i]) {
		case "from":
			if i+1 >= len(args) {
				return "ERR `from` needs a value"
			}
			v, err := parseRegVal(args[i+1])
			if err != nil {
				return "ERR bad from: " + err.Error()
			}
			w.HasFrom = true
			w.From = v
			i += 2
		case "to":
			if i+1 >= len(args) {
				return "ERR `to` needs a value"
			}
			v, err := parseRegVal(args[i+1])
			if err != nil {
				return "ERR bad to: " + err.Error()
			}
			w.HasTo = true
			w.To = v
			i += 2
		default:
			return "ERR unknown clause: " + args[i]
		}
	}
	// Validate the register name against the live cpuState adapter
	// — catches typos at set-time rather than silently never firing.
	if _, ok := (cpuState{cpu: d.emu.cpu, mem: d.emu.mem}).Reg(reg); !ok {
		return "ERR unknown register: " + reg
	}
	d.regWatches.Add(w)
	return "OK " + w.FormatLine()
}

func (d *remoteDebugger) cmdListWatches() string {
	ws := d.regWatches.List()
	if len(ws) == 0 {
		return "OK (no watches)"
	}
	var sb strings.Builder
	sb.WriteString("OK\r\n")
	for _, w := range ws {
		fmt.Fprintf(&sb, "  %s\r\n", w.FormatLine())
	}
	return strings.TrimRight(sb.String(), "\r\n")
}

func (d *remoteDebugger) cmdClearWatch(args []string) string {
	if len(args) == 0 {
		d.regWatches.Clear()
		return "OK all watches cleared"
	}
	reg := strings.ToLower(args[0])
	d.regWatches.Remove(reg)
	return "OK removed " + reg
}

// parseRegVal accepts hex (with $ or 0x prefix, default), or a bare
// decimal number suffixed with 'd'. Mirrors the parsing the visual
// debugger's parseUserHex does so the two surfaces agree.
func parseRegVal(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "D") {
		return strconv.ParseInt(s[:len(s)-1], 10, 64)
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "$"), "0x")
	return strconv.ParseInt(s, 16, 64)
}
