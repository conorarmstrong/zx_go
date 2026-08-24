package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The CTC was complete and pinned against the FPGA VHDL for months while no
// program could reach it: nothing constructed the device and no port decoded to
// it. This is the end-to-end check that it is now part of the machine, written
// from the guest's side rather than the wiring's.
//
// A guest programs channel 0 at $183B with a control word (D2 set: a time
// constant follows) and then the constant, and reads the port back to see the
// counter. If any link in the chain is missing the read returns floating-bus
// junk instead.
func TestCTCIsReachableFromAGuestOnTheNext(t *testing.T) {
	e, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Fatalf("newEmulator(Next): %v", err)
	}
	if e.nextCTC == nil {
		t.Fatal("a Next emulator has no CTC bank")
	}

	const port = uint16(0x183B)
	e.ula.WritePort(port, 0x05|0x40) // control: counter mode, TC follows
	e.ula.WritePort(port, 0x2A)      // time constant

	got, handled := e.ula.ReadPort(port)
	if !handled {
		t.Fatal("a read of $183B was not handled: the CTC is not routed")
	}
	if got != 0x2A {
		t.Errorf("counter read back as $%02X, want the loaded $2A", got)
	}
}

// And it must not exist on machines that do not have one, or a 48K program
// reading $183B would get a CTC counter where the floating bus belongs.
func TestCTCIsAbsentOnNonNextMachines(t *testing.T) {
	e, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator(48K): %v", err)
	}
	if e.nextCTC != nil {
		t.Error("a 48K emulator has a CTC bank")
	}
}
