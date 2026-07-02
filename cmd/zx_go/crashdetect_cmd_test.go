package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Covers the live `crash-detect` command dispatcher and its
// status / enable / disable / set sub-handlers. enable/set wire a
// pre-fetch hook onto a real CPU, so the fixture supplies one.

func newRemoteWithCPU(t *testing.T) *remoteDebugger {
	t.Helper()
	dir := newTestRomDir(t)
	mem, err := memory.New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cpu := z80.New(mem, nil)
	return &remoteDebugger{emu: &emulator{cpu: cpu, mem: mem}}
}

func TestCmdCrashDetect_StatusAndUnknown(t *testing.T) {
	d := newRemoteWithCPU(t)
	if got := d.cmdCrashDetect(nil); got != "OK crash-detect off" {
		t.Errorf("no-args = %q, want \"OK crash-detect off\"", got)
	}
	if got := d.cmdCrashDetect([]string{"status"}); got != "OK crash-detect off" {
		t.Errorf("status = %q", got)
	}
	if got := d.cmdCrashDetect([]string{"bogus"}); !strings.HasPrefix(got, "ERR crash-detect: unknown") {
		t.Errorf("unknown subcmd = %q", got)
	}
}

func TestCmdCrashDetect_PauseToggle(t *testing.T) {
	d := newRemoteWithCPU(t)
	if got := d.cmdCrashDetect([]string{"pause"}); got != "OK crash-detect pause-on-fire=true" {
		t.Errorf("first toggle = %q", got)
	}
	if got := d.cmdCrashDetect([]string{"pause-on-fire"}); got != "OK crash-detect pause-on-fire=false" {
		t.Errorf("second toggle = %q", got)
	}
}

func TestCmdCrashDetect_EnableDisable(t *testing.T) {
	d := newRemoteWithCPU(t)
	got := d.cmdCrashDetect([]string{"on"})
	if !strings.HasPrefix(got, "OK crash-detect on:") {
		t.Fatalf("enable = %q", got)
	}
	if d.crashDetect == nil {
		t.Fatal("crashDetect nil after enable")
	}
	// Re-enabling replaces the hook cleanly.
	if got := d.cmdCrashDetect([]string{"enable"}); !strings.HasPrefix(got, "OK crash-detect on:") {
		t.Errorf("re-enable = %q", got)
	}
	if got := d.cmdCrashDetect([]string{"off"}); got != "OK crash-detect off" {
		t.Errorf("disable = %q", got)
	}
	if d.crashDetect != nil {
		t.Error("crashDetect not cleared after off")
	}
	// Disable when already off.
	if got := d.cmdCrashDetect([]string{"off"}); got != "OK crash-detect already off" {
		t.Errorf("double-off = %q", got)
	}
}

func TestCrashDetectSet(t *testing.T) {
	d := newRemoteWithCPU(t)

	if got := d.crashDetectSet([]string{"nop-slide"}); !strings.HasPrefix(got, "ERR crash-detect set: need") {
		t.Errorf("missing value = %q", got)
	}
	if got := d.crashDetectSet([]string{"nop-slide", "-1"}); !strings.HasPrefix(got, "ERR nop-slide") {
		t.Errorf("bad N = %q", got)
	}
	if got := d.crashDetectSet([]string{"nop-slide", "8abc"}); !strings.HasPrefix(got, "ERR nop-slide") {
		t.Errorf("trailing garbage after N = %q, want ERR nop-slide", got)
	}
	if got := d.crashDetectSet([]string{"sp-low", "zz"}); !strings.HasPrefix(got, "ERR sp-low") {
		t.Errorf("bad sp-low = %q", got)
	}
	if got := d.crashDetectSet([]string{"sp-high", "zz"}); !strings.HasPrefix(got, "ERR sp-high") {
		t.Errorf("bad sp-high = %q", got)
	}
	if got := d.crashDetectSet([]string{"halt-no-iff", "maybe"}); !strings.HasPrefix(got, "ERR halt-no-iff") {
		t.Errorf("bad halt-no-iff = %q", got)
	}
	if got := d.crashDetectSet([]string{"bogus", "x"}); !strings.HasPrefix(got, "ERR crash-detect set: unknown KEY") {
		t.Errorf("unknown key = %q", got)
	}

	// Success path: nop-slide enables + appears in status.
	got := d.crashDetectSet([]string{"nop-slide", "8"})
	if !strings.Contains(got, "nop-slide=8") {
		t.Errorf("set nop-slide = %q, want nop-slide=8", got)
	}
	// sp-low / sp-high deltas accumulate onto the active config.
	if got := d.crashDetectSet([]string{"sp-low", "4000"}); !strings.Contains(got, "sp-low=$4000") {
		t.Errorf("set sp-low = %q", got)
	}
	if got := d.crashDetectSet([]string{"halt-no-iff", "on"}); !strings.Contains(got, "halt-no-iff") {
		t.Errorf("set halt-no-iff = %q", got)
	}
	// pc-region add then clear.
	if got := d.crashDetectSet([]string{"pc-region", "trap@1D47-1D4A"}); !strings.Contains(got, "pc-region[trap") {
		t.Errorf("set pc-region = %q", got)
	}
	if got := d.crashDetectSet([]string{"pc-region", "clear"}); strings.Contains(got, "pc-region[") {
		t.Errorf("after clear still has pc-region: %q", got)
	}
}
