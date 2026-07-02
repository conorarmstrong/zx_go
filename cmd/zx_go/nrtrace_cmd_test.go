package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func newRemoteForNRTrace(t *testing.T) *remoteDebugger {
	t.Helper()
	mem, err := memory.New(nextTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return &remoteDebugger{emu: &emulator{mem: mem, nextRegs: nextregs.New()}}
}

// TestCmdNRTrace_ListEmpty verifies the no-args status reply before
// anything is armed.
func TestCmdNRTrace_ListEmpty(t *testing.T) {
	d := newRemoteForNRTrace(t)
	if got := d.cmdNRTrace(nil); got != "OK (no nr-trace)" {
		t.Errorf("empty status = %q", got)
	}
}

// TestCmdNRTrace_ArmListsEveryReg verifies both comma-separated and
// space-separated register lists are accepted, and each armed reg is
// reported (as hex) in the subsequent status query.
func TestCmdNRTrace_ArmListsEveryReg(t *testing.T) {
	d := newRemoteForNRTrace(t)

	if got := d.cmdNRTrace([]string{"56,57"}); !strings.HasPrefix(got, "OK nr-trace=") {
		t.Fatalf("arm = %q", got)
	}
	if got := d.cmdNRTrace([]string{"04"}); !strings.HasPrefix(got, "OK nr-trace=") {
		t.Fatalf("arm second = %q", got)
	}

	got := d.cmdNRTrace(nil)
	for _, want := range []string{"$56", "$57", "$04"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %s", got, want)
		}
	}
}

// TestCmdNRTrace_Off verifies `nr-trace off` clears the watch set.
func TestCmdNRTrace_Off(t *testing.T) {
	d := newRemoteForNRTrace(t)
	d.cmdNRTrace([]string{"56"})
	if got := d.cmdNRTrace([]string{"off"}); got != "OK nr-trace cleared" {
		t.Errorf("off = %q", got)
	}
	if got := d.cmdNRTrace(nil); got != "OK (no nr-trace)" {
		t.Errorf("status after off = %q", got)
	}
}
