package main

import (
	"strings"
	"testing"
)

// Covers the bp-first-entry + snapshot-on-bp command parsers and
// their pure helpers. fireBPFirstEntry is exercised with
// snapshot-on-bp disabled so the heavyweight szx-write path is
// skipped (it needs a full emulator) while the arm/clear/pause logic
// is still covered.

func TestCmdSnapshotOnBP(t *testing.T) {
	d := &remoteDebugger{}
	// Disable (no args).
	if got := d.cmdSnapshotOnBP(nil); got != "OK snapshot-on-bp disabled" {
		t.Errorf("disable = %q", got)
	}
	// Whitespace-only also disables.
	if got := d.cmdSnapshotOnBP([]string{"  "}); got != "OK snapshot-on-bp disabled" {
		t.Errorf("blank dir = %q", got)
	}
	// Non-existent dir → ERR.
	if got := d.cmdSnapshotOnBP([]string{"/no/such/dir/xyz"}); !strings.HasPrefix(got, "ERR directory does not exist") {
		t.Errorf("missing dir = %q", got)
	}
	// Existing dir → OK, and the global pointer is set.
	dir := t.TempDir()
	if got := d.cmdSnapshotOnBP([]string{dir}); got != "OK snapshot-on-bp dir="+dir {
		t.Errorf("existing dir = %q", got)
	}
	if p := snapshotOnBPDir.Load(); p == nil || *p != dir {
		t.Error("snapshotOnBPDir not stored")
	}
	// Disarm again so the global doesn't leak into other tests.
	d.cmdSnapshotOnBP(nil)
}

func TestCmdBPFirstEntry(t *testing.T) {
	d := &remoteDebugger{}

	if got := d.cmdBPFirstEntry(nil); got != "OK (no bp-first-entry armed)" {
		t.Errorf("inspect-empty = %q", got)
	}
	// Arm a range with a snap prefix; should clear paused.
	d.paused.Store(true)
	got := d.cmdBPFirstEntry([]string{"$C000-$DFFF", "snap=nop"})
	if !strings.Contains(got, "$C000-$DFFF") || !strings.Contains(got, "snap=nop") || !strings.HasSuffix(got, "running") {
		t.Errorf("arm = %q", got)
	}
	if d.paused.Load() {
		t.Error("arm should clear paused")
	}
	// Inspect while armed.
	if got := d.cmdBPFirstEntry(nil); !strings.Contains(got, "(armed)") || !strings.Contains(got, "snap=nop") {
		t.Errorf("inspect-armed = %q", got)
	}
	// Bad range.
	if got := d.cmdBPFirstEntry([]string{"$zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("bad range = %q", got)
	}
	// Unknown arg.
	if got := d.cmdBPFirstEntry([]string{"$0038", "frob"}); !strings.HasPrefix(got, "ERR unknown arg") {
		t.Errorf("unknown arg = %q", got)
	}
	// Clear.
	if got := d.cmdBPFirstEntry([]string{"off"}); got != "OK bp-first-entry cleared" {
		t.Errorf("clear = %q", got)
	}
	if d.bpFirstEntry.Load() != nil {
		t.Error("clear should nil the spec")
	}
}

func TestFireBPFirstEntry(t *testing.T) {
	d := &remoteDebugger{}
	snapshotOnBPDir.Store(nil) // ensure the szx-write path is skipped
	spec := &bpFirstEntrySpec{lo: 0xC000, hi: 0xDFFF, prefix: "x"}
	d.bpFirstEntry.Store(spec)
	d.fireBPFirstEntry(0xC100, spec)
	if d.bpFirstEntry.Load() != nil {
		t.Error("fire should self-clear the spec")
	}
	if !d.paused.Load() {
		t.Error("fire should pause the CPU")
	}
}
