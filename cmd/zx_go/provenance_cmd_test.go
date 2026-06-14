package main

import (
	"strings"
	"testing"
)

// TestCmdProvenanceArmAndQuery covers the live command path: arming
// installs the WriteObserver, a subsequent CPU write is recorded, and
// `provenance $ADDR` reports the writing instruction.
func TestCmdProvenanceArmAndQuery(t *testing.T) {
	d := newRemoteWithCPU(t)

	if got := d.cmdProvenance(nil); got != "OK provenance off" {
		t.Errorf("no-args = %q, want \"OK provenance off\"", got)
	}
	if got := d.cmdProvenance([]string{"on"}); !strings.Contains(got, "on") {
		t.Errorf("arm = %q, want it to confirm armed", got)
	}
	if !d.provenance.enabled.Load() {
		t.Fatal("provenance not enabled after `on`")
	}

	// A CPU write to RAM ($C000 is RAM on 128K) must be recorded.
	d.emu.cpu.PC = 0x1234
	d.emu.mem.Write(0xC000, 0xAB)

	got := d.cmdProvenance([]string{"$C000"})
	if !strings.Contains(got, "val=$AB") || !strings.Contains(got, "$1234") {
		t.Errorf("query = %q, want it to name val=$AB written at PC=$1234", got)
	}

	// Disarm stops recording.
	d.cmdProvenance([]string{"off"})
	if d.provenance.enabled.Load() {
		t.Error("provenance still enabled after `off`")
	}
}

// TestCmdWhyPCRequiresArmed verifies why-pc refuses to run when the
// tracker was never armed (no data to walk).
func TestCmdWhyPCRequiresArmed(t *testing.T) {
	d := newRemoteWithCPU(t)
	got := d.cmdWhyPC(nil)
	if !strings.HasPrefix(got, "ERR") {
		t.Errorf("why-pc unarmed = %q, want an ERR about not armed", got)
	}
}

// TestCmdProvenancePhys covers the physical-pool query: after arming
// and recording a physical write, `provenance-phys BANK OFF` reports
// the writer; bad args yield an ERR.
func TestCmdProvenancePhys(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.cmdProvenance([]string{"on"}) // allocates phys map + enables

	if got := d.cmdProvenancePhys([]string{"$6F"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("one-arg = %q, want ERR usage", got)
	}

	// $C7 written to physical bank 111 ($6F), offset $002D.
	d.emu.cpu.PC = 0x4321
	d.provenance.recordPhys(111, 0x002D, 0xC7, 777, 0x4321)

	got := d.cmdProvenancePhys([]string{"$6F", "$2D"})
	if !strings.Contains(got, "val=$C7") || !strings.Contains(got, "$4321") {
		t.Errorf("query = %q, want val=$C7 written at PC=$4321", got)
	}
}

// TestCmdProvenanceUntouchedAddr reports a clear message for an address
// nothing has written since arming.
func TestCmdProvenanceUntouchedAddr(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.cmdProvenance([]string{"on"})
	got := d.cmdProvenance([]string{"$8000"})
	if !strings.Contains(got, "no recorded writer") {
		t.Errorf("untouched query = %q, want \"no recorded writer\"", got)
	}
}
