package main

import (
	"log/slog"
	"strings"
)

// runBPAction executes the semicolon-separated command list attached
// to a breakpoint via `bp ADDR ... do "..."`. Each command is parsed
// and dispatched the same way the telnet handler does for an
// interactively-typed command, with the response emitted as one
// slog.Info("bp-action") event per command.
//
// Called from the BP-hit path in BreakpointCheck immediately after
// the pause flag is set. Commands run AGAINST a paused CPU (the pause
// happened on the same M1 fetch as the action) so register-reading
// commands like `regs` see the post-hit state.
//
// Recursion guard: actions cannot themselves create new BPs whose
// actions would fire on the same hit — the BP-hit path returns true
// and the CPU loop exits before any new BP takes effect on this
// instruction. Re-entry safety relies on this.
func (d *remoteDebugger) runBPAction(pc uint16, action string) {
	for _, raw := range strings.Split(action, ";") {
		cmd := strings.TrimSpace(raw)
		if cmd == "" {
			continue
		}
		// Reuse the telnet dispatch path. handleCommand acquires
		// d.mu internally; the BP-hit path runs from the CPU goroutine
		// which doesn't hold d.mu, so this is safe.
		resp := d.handleCommand(cmd)
		slog.Info("bp-action",
			"pc", "$"+formatHex16(pc),
			"cmd", cmd,
			"resp", oneLine(resp))
	}
}

// oneLine collapses a multi-line debugger response into a single line
// for slog readability. The full response is still complete; this
// just replaces \r\n with `\n` literally and trims to a sensible
// length so log scanners stay usable.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " | ")
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

// formatHex16 formats a uint16 as 4-digit uppercase hex without the
// "$" prefix.
func formatHex16(v uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{
		digits[(v>>12)&0xF],
		digits[(v>>8)&0xF],
		digits[(v>>4)&0xF],
		digits[v&0xF],
	})
}
