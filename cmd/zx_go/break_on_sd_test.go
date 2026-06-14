package main

import (
	"strings"
	"testing"
)

// break-on-sd arms a one-shot breakpoint that fires when the SD card
// receives a specific command. The NextZXOS DOS SD driver runs in a
// paged region whose static disassembly is bank-misaligned vs
// execution, so address breakpoints there don't fire — but the SD layer
// sees every command with the live cpu.PC, so this catches the exact
// moment a command (e.g. the mount re-init CMD0, or CMD17 at the boot
// sector LBA $3F) is issued.
func TestBreakOnSD(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true) // skip the implicit dispatch pause-wait

	// Arm CMD0.
	if got := d.handleCommand("break-on-sd CMD0"); !strings.HasPrefix(got, "OK break-on-sd CMD0") {
		t.Fatalf("arm CMD0 = %q", got)
	}
	// A different command must NOT fire it.
	d.paused.Store(false)
	d.checkSDCommand(8, 0x1AA, false)
	if d.paused.Load() {
		t.Fatal("CMD8 wrongly fired a CMD0 break")
	}
	// CMD0 fires.
	d.checkSDCommand(0, 0, false)
	if !d.paused.Load() {
		t.Fatal("CMD0 did not fire the armed break")
	}
	// One-shot: spec cleared after firing.
	if d.sdBreak.Load() != nil {
		t.Fatal("break-on-sd should be one-shot (spec not cleared)")
	}

	// Arg-specific: CMD17 only at LBA $3F.
	d.paused.Store(false)
	d.handleCommand("break-on-sd CMD17 $3F")
	d.checkSDCommand(17, 0x00, false)
	if d.paused.Load() {
		t.Fatal("CMD17 at arg $00 wrongly fired the $3F break")
	}
	d.checkSDCommand(17, 0x3F, false)
	if !d.paused.Load() {
		t.Fatal("CMD17 at arg $3F did not fire")
	}

	// ACMD is distinct from a plain CMD of the same number.
	d.paused.Store(false)
	d.handleCommand("break-on-sd ACMD41")
	d.checkSDCommand(41, 0x40000000, false) // plain CMD41
	if d.paused.Load() {
		t.Fatal("plain CMD41 wrongly fired an ACMD41 break")
	}
	d.checkSDCommand(41, 0x40000000, true)
	if !d.paused.Load() {
		t.Fatal("ACMD41 did not fire")
	}

	// off clears the spec.
	d.handleCommand("break-on-sd CMD0")
	if got := d.handleCommand("break-on-sd off"); !strings.HasPrefix(got, "OK break-on-sd off") {
		t.Fatalf("off = %q", got)
	}
	if d.sdBreak.Load() != nil {
		t.Fatal("break-on-sd off did not clear the spec")
	}

	// Bad spec is rejected.
	if got := d.handleCommand("break-on-sd FOO"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("bad spec = %q, want ERR", got)
	}
}
