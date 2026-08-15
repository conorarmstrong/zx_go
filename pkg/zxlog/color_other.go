//go:build !windows

package zxlog

// enableVirtualTerminal is a no-op away from Windows: a terminal that
// IsTerminal accepts already interprets ANSI.
func enableVirtualTerminal(uintptr) error { return nil }
