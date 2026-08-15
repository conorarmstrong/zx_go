package zxlog

import (
	"golang.org/x/term"
)

// isTerminal and enableANSI are the two platform probes behind the
// colour decision, kept as vars so tests can substitute them.
var (
	isTerminal = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }
	enableANSI = enableVirtualTerminal
)

// colorEnabled reports whether ANSI colour should be written to fd.
//
// Being a terminal is not sufficient on Windows. A console handle
// answers GetConsoleMode, so it looks like a terminal to IsTerminal,
// yet it prints escape sequences literally until
// ENABLE_VIRTUAL_TERMINAL_PROCESSING is set. Asking the platform to
// turn that on, and believing its answer, is what stops the banner
// arriving as a screenful of "<-[38;5;196m".
func colorEnabled(fd uintptr) bool {
	if !isTerminal(fd) {
		return false
	}
	return enableANSI(fd) == nil
}
