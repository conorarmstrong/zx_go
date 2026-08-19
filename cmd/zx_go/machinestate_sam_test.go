package main

import (
	"bytes"
	"strings"
	"testing"
)

// The SAM Coupé used to return an EMPTY registry, so rewind and time travel
// silently did nothing on it: the ring captured a machine with no devices in it
// and restoring one put nothing back. This is the regression guard.

func newSAMForState(t *testing.T) *emulator {
	t.Helper()
	emu, err := newSamEmulator()
	if err != nil {
		t.Fatalf("newSamEmulator: %v", err)
	}
	return emu
}

// The SAM's own devices are registered, and the Spectrum-typed stand-ins that
// exist only for the GUI's menu code are not. Registering "memory" or
// "keyboard" here would produce a capture that restores nothing the machine
// runs on, which is worse than having none.
func TestStateRegistryDeviceSetSAM(t *testing.T) {
	emu := newSAMForState(t)
	want := []string{
		"cpu",
		"sam.asic", "sam.fdc1", "sam.fdc2", "sam.keyboard", "sam.memory", "sam.saa1099",
	}
	got := registeredDevices(emu)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("device set:\n got %v\nwant %v", got, want)
	}
	for _, standIn := range []string{"memory", "keyboard", "ula"} {
		if hasDevice(got, standIn) {
			t.Errorf("%q is registered: that is the inert Spectrum stand-in, not the SAM's own", standIn)
		}
	}
}

// Capture, run on, restore, capture again. The two captures match exactly when
// every registered device gave back what it was holding.
//
// The "the machine actually moved" guard is not ceremony: without it a machine
// whose devices never change while running would pass no matter what Restore
// did, which is precisely the state the SAM was in before it had any.
func TestStateRegistryRoundTripsOnTheSAM(t *testing.T) {
	emu := newSAMForState(t)
	for i := 0; i < 60; i++ {
		emu.sam.RunFrame()
	}

	reg := emu.stateRegistry()
	before := reg.Capture()

	for i := 0; i < 20; i++ {
		emu.sam.RunFrame()
	}
	if moved := reg.Capture(); bytes.Equal(before.Bytes(), moved.Bytes()) {
		t.Fatal("no registered device changed over 20 SAM frames — the fixture is inert, " +
			"not the restore correct")
	}

	if err := reg.Restore(before); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if after := reg.Capture(); !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Error("the restored SAM does not re-capture as the machine that was captured")
	}
}
