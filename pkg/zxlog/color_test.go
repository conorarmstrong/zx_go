package zxlog

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// A Windows console handle answers GetConsoleMode, so term.IsTerminal
// reports it as a terminal, but it renders ANSI literally until
// ENABLE_VIRTUAL_TERMINAL_PROCESSING is set. Colour therefore has to
// depend on both facts, not just the first.
func TestColorEnabledNeedsATerminalAndWorkingANSI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		ansiErr  error
		want     bool
	}{
		{"redirected output stays plain", false, nil, false},
		{"terminal that accepts ANSI is coloured", true, nil, true},
		{"terminal that refuses ANSI stays plain", true, errors.New("SetConsoleMode: not supported"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer swapColorProbes(func(uintptr) bool { return tc.terminal },
				func(uintptr) error { return tc.ansiErr })()

			if got := colorEnabled(0); got != tc.want {
				t.Errorf("colorEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// A console that cannot interpret ANSI must get the banner as plain
// text rather than a screenful of escape codes.
func TestRenderBannerPlainWhenANSIUnavailable(t *testing.T) {
	var buf bytes.Buffer
	renderBannerWith(&buf, off)

	if out := buf.String(); strings.Contains(out, "\x1b[") {
		t.Errorf("plain banner still contains an escape sequence:\n%q", out)
	}
	if !strings.Contains(buf.String(), Version) {
		t.Errorf("plain banner lost its version line:\n%s", buf.String())
	}
}

// swapColorProbes installs test doubles for the two platform probes
// and returns a func restoring the originals.
func swapColorProbes(term func(uintptr) bool, ansi func(uintptr) error) func() {
	oldTerm, oldANSI := isTerminal, enableANSI
	isTerminal, enableANSI = term, ansi
	return func() { isTerminal, enableANSI = oldTerm, oldANSI }
}
