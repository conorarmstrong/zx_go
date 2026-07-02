package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestTTRewindRestoresHaltedAndInsnCounter verifies tt-rewind
// round-trips the CPU's Halted/IFF1 flags — a rewind target captured
// while running must not leave the CPU halted with interrupts
// disabled — and moves the instruction counter back to the
// snapshot's insn so subsequent tt operations and insn-gated
// diagnostics see a consistent timeline.
func TestTTRewindRestoresHaltedAndInsnCounter(t *testing.T) {
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	defer func() { cliFlagsActive = prev }()

	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	tt := newTimeTravelBuffer(emu, 1000, 8)

	// Run a little, then capture a snapshot in a known-good state:
	// running (not halted), interrupts enabled.
	for i := 0; i < 50; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	emu.cpu.Halted = false
	emu.cpu.IFF1, emu.cpu.IFF2 = true, true
	wantInsn := emu.cpu.InstructionCount()
	tt.capture("checkpoint", wantInsn)

	// Wreck the live state the way the reported defect observed it:
	// halted with interrupts off, and the counter far ahead.
	for i := 0; i < 20; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	emu.cpu.Halted = true
	emu.cpu.IFF1, emu.cpu.IFF2 = false, false

	e := tt.FindAtOrBefore(wantInsn)
	if e == nil {
		t.Fatal("no snapshot found at-or-before the checkpoint")
	}
	if err := tt.Restore(e); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if emu.cpu.Halted {
		t.Error("Restore left Halted=true — the rewound CPU cannot resume")
	}
	if !emu.cpu.IFF1 {
		t.Error("Restore did not bring IFF1 back to the snapshot value (true)")
	}
	if got := emu.cpu.InstructionCount(); got != wantInsn {
		t.Errorf("instruction counter not rewound: got %d, want %d", got, wantInsn)
	}
}
