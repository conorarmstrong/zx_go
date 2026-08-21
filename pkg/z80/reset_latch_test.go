package z80

import "testing"

// A hardware /RESET drops the interrupt request lines with everything
// else. Leaving PendingNMI latched let a reboot ordered between the
// NMI button and its acknowledgement run the first instruction at
// $0000 and then jump straight to $0066, as if the button were still
// down: on a Next that opens the NMI Browser in the middle of bootrom,
// and with a Multiface it pages the MF ROM in over the fresh machine.
func TestResetDropsPendingNMI(t *testing.T) {
	c, _ := createTestCPU()
	c.PendingNMI.Store(true)
	c.Reset()
	if c.PendingNMI.Load() {
		t.Error("PendingNMI still latched after Reset")
	}
}

func TestResetDropsPendingIRQ(t *testing.T) {
	c, _ := createTestCPU()
	c.IRQPending.Store(true)
	c.Reset()
	if c.IRQPending.Load() {
		t.Error("IRQPending still latched after Reset")
	}
}

// eiDelay defers acknowledgement by one instruction after an EI. A
// reset in that window left the deferral armed against the first
// instruction of the freshly reset machine.
func TestResetDropsTheEIDeferral(t *testing.T) {
	c, _ := createTestCPU()
	c.eiDelay = true
	c.Reset()
	if c.eiDelay {
		t.Error("eiDelay still armed after Reset")
	}
}
