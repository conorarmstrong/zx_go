package main

import (
	"strings"
	"testing"
)

// TestCmdTraceNRDeltas_SingleRegFFNotReportedAsAll guards against a
// sentinel collision: list() previously used []byte{0xFF} to signal
// "all regs armed", but $FF is also a real (reserved) NextReg number.
// Watching only $FF must be reported as watching $FF, not as "all".
func TestCmdTraceNRDeltas_SingleRegFFNotReportedAsAll(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.nrDeltas.set.add(0xFF)

	got := d.cmdTraceNRDeltas(nil)
	if strings.Contains(got, "all") {
		t.Errorf("watching only $FF reported as \"all\": %q", got)
	}
	if !strings.Contains(got, "$FF") {
		t.Errorf("status should name $FF: %q", got)
	}
}

// TestCmdTraceNRDeltas_AllStillReportsAll verifies the real "all" mode
// (armed via `trace-nextreg-deltas all`) still reports as "all".
func TestCmdTraceNRDeltas_AllStillReportsAll(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.nrDeltas.set.addAll()

	got := d.cmdTraceNRDeltas(nil)
	if !strings.Contains(got, "all") {
		t.Errorf("addAll() status should say \"all\": %q", got)
	}
}

// TestCmdTraceNRDeltas_MultipleRegsListed verifies a normal multi-reg
// watch set still lists every armed register.
func TestCmdTraceNRDeltas_MultipleRegsListed(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.nrDeltas.set.add(0x04)
	d.nrDeltas.set.add(0x56)

	got := d.cmdTraceNRDeltas(nil)
	if !strings.Contains(got, "$04") || !strings.Contains(got, "$56") {
		t.Errorf("status should list both $04 and $56: %q", got)
	}
}
