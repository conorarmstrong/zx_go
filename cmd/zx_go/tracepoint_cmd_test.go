package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// These tests cover the tracepoint / watch-zero / history-graph command
// handlers plus the sortUint16s helper. These operate on atomic maps
// and global symbol annotation only, so a bare remoteDebugger works.

func TestCmdTpLifecycle(t *testing.T) {
	d := &remoteDebugger{}

	if got := d.cmdTp(nil); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("tp no-arg = %q", got)
	}
	if got := d.cmdTp([]string{"zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("tp bad addr = %q", got)
	}
	if got := d.cmdListTp(); got != "OK (no tracepoints)" {
		t.Errorf("empty list-tp = %q", got)
	}
	if got := d.cmdTp([]string{"8000"}); !strings.Contains(got, "tp @") {
		t.Errorf("tp = %q", got)
	}
	d.cmdTp([]string{"4000"})
	list := d.cmdListTp()
	// Ascending sort: $4000 line precedes $8000 line.
	if !strings.Contains(list, "hits=0") || strings.Index(list, "4000") > strings.Index(list, "8000") {
		t.Errorf("list-tp ordering = %q", list)
	}

	// Clear one.
	if got := d.cmdClearTp([]string{"4000"}); !strings.Contains(got, "removed tp @") {
		t.Errorf("clear-tp one = %q", got)
	}
	if got := d.cmdClearTp([]string{"zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("clear-tp bad addr = %q", got)
	}
	// Clear all.
	if got := d.cmdClearTp(nil); got != "OK all tracepoints cleared" {
		t.Errorf("clear-tp all = %q", got)
	}
	// Clear when none remain.
	if got := d.cmdClearTp([]string{"8000"}); got != "OK (no tracepoints)" {
		t.Errorf("clear-tp empty = %q", got)
	}
}

func TestCmdWatchZero(t *testing.T) {
	d := &remoteDebugger{}
	if got := d.cmdWatchZero(nil); got != "OK watch-zero=off" {
		t.Errorf("status default = %q", got)
	}
	if got := d.cmdWatchZero([]string{"on"}); got != "OK watch-zero=on" {
		t.Errorf("on = %q", got)
	}
	if got := d.cmdWatchZero(nil); got != "OK watch-zero=on" {
		t.Errorf("status after on = %q", got)
	}
	if got := d.cmdWatchZero([]string{"off"}); got != "OK watch-zero=off" {
		t.Errorf("off = %q", got)
	}
	if got := d.cmdWatchZero([]string{"maybe"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("bad arg = %q", got)
	}
}

func TestCmdHistoryAndGraph_Disabled(t *testing.T) {
	d := &remoteDebugger{} // history nil
	if got := d.cmdHistory(nil); !strings.HasPrefix(got, "ERR history disabled") {
		t.Errorf("history disabled = %q", got)
	}
	if got := d.cmdGraph(nil, debugger.SourceCall, "callgraph"); !strings.HasPrefix(got, "ERR history disabled") {
		t.Errorf("graph disabled = %q", got)
	}
}

func TestSortUint16s(t *testing.T) {
	s := []uint16{0x8000, 0x0100, 0xFFFF, 0x4242, 0x0001}
	sortUint16s(s)
	want := []uint16{0x0001, 0x0100, 0x4242, 0x8000, 0xFFFF}
	for i := range want {
		if s[i] != want[i] {
			t.Errorf("sortUint16s[%d] = $%04X, want $%04X", i, s[i], want[i])
		}
	}
	// Empty + single-element are no-ops.
	sortUint16s([]uint16{})
	sortUint16s([]uint16{0x1234})
}
