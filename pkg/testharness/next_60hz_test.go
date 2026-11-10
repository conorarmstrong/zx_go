package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NR$05 bit 2 is the effective 50/60 Hz video mode. It must switch the
// ULA frame geometry, the memory contention origin and the CPU's
// frame-INT assert point together; wiring only one of them leaves the
// machine self-inconsistent at 60 Hz.
func TestNext60HzSwitchUpdatesULAMemoryAndINT(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	if h.ULA().SixtyHz() || h.MemoryBus().SixtyHz() {
		t.Fatal("60 Hz should be off at reset")
	}
	if got := h.ULA().FrameTStates(); got != 70908 {
		t.Fatalf("initial frame = %d, want 70908", got)
	}
	if got := h.CPU().IntAssertTstate; got != 291 {
		t.Fatalf("initial INT assert = %d, want 291 (+3 50 Hz)", got)
	}

	// Write NR$05 bit 2 (60 Hz). The joystick/scandoubler bits are 0.
	h.NextRegs().WriteReg(0x05, 0x04)

	if !h.ULA().SixtyHz() || !h.MemoryBus().SixtyHz() {
		t.Error("60 Hz flag did not reach ULA and memory")
	}
	if got := h.ULA().FrameTStates(); got != 60192 {
		t.Errorf("60 Hz frame = %d, want 60192 (+3 264x456/2)", got)
	}
	if got := h.CPU().IntAssertTstate; got != 63 {
		t.Errorf("60 Hz INT assert = %d, want 63 (+3 c_int_h 126/2)", got)
	}
	// NR$05 bit 2 reads back; the unmodelled scandoubler bit 0 does not.
	if got := h.NextRegs().ReadReg(0x05); got != 0x04 {
		t.Errorf("NR$05 read-back = $%02X, want $04", got)
	}

	// Back to 50 Hz: every component must follow.
	h.NextRegs().WriteReg(0x05, 0x00)
	if h.ULA().SixtyHz() || h.MemoryBus().SixtyHz() {
		t.Error("50 Hz flag did not clear")
	}
	if got := h.ULA().FrameTStates(); got != 70908 {
		t.Errorf("back to 50 Hz frame = %d, want 70908", got)
	}
	if got := h.CPU().IntAssertTstate; got != 291 {
		t.Errorf("back to 50 Hz INT assert = %d, want 291", got)
	}
}
