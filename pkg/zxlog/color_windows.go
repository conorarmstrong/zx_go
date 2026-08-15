//go:build windows

package zxlog

import "golang.org/x/sys/windows"

// enableVirtualTerminal asks the Windows console to interpret ANSI
// escape sequences. Consoles from Windows 10 support it but do not
// all enable it by default, and older ones cannot: either way the
// error is the signal to fall back to plain output.
func enableVirtualTerminal(fd uintptr) error {
	h := windows.Handle(fd)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return err
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return nil
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
}
