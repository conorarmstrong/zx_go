package next

import "testing"

// IM2Driver turns the daisy chain's per-clock FSM into the three CPU bus events
// an instruction-granular emulator can actually observe. It is exact rather
// than approximate, and the reason is worth stating: every transition in
// deviceNextStateIdx depends only on its inputs and the latched request, with
// no counter or timer anywhere, so between input changes the FSM is a fixed
// point. Ticking it when an input changes therefore reaches the same states as
// ticking it every clock; only the wall-clock moment differs, and that is
// already instruction-granular for everything else in this emulator.
//
// The acknowledge is the one place a sequence is needed rather than a single
// tick: S0 -> REQ wants /M1 high, REQ -> ACK wants M1 and IORQ together, and
// ACK -> ISR wants /M1 high again (im2.go's device FSM). The driver plays that
// sequence out at the moment the CPU accepts.
func TestIM2DriverDeliversAVectorAndReleasesOnRETI(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true) // NR$C0 bit 0: hardware IM2 rather than pulse

	// Nothing pending: no interrupt, and nothing to acknowledge.
	if d.INTAsserted() {
		t.Fatal("/INT asserted with no source requesting")
	}
	if _, ok := d.Acknowledge(); ok {
		t.Error("a device answered the acknowledge cycle with nothing requesting")
	}

	// The ULA's frame interrupt is priority 11. Enable before raising: see
	// TestIM2DriverLatchesOnTheRequestEdge.
	d.SetEnabled(IM2SourceULA, true)
	d.Raise(IM2SourceULA)
	if !d.INTAsserted() {
		t.Fatal("/INT not asserted after the ULA raised its request")
	}

	vec, ok := d.Acknowledge()
	if !ok {
		t.Fatal("no device answered the acknowledge cycle")
	}
	// nr_c0_im2_vector & vector & '0' (zxnext.vhd:1999): NR$C0 bits 7:5 on top,
	// the 4-bit chain index, then a low zero. With NR$C0 at 0 that is 11<<1.
	if want := byte(IM2SourceULA << 1); vec != want {
		t.Errorf("vector = $%02X, want $%02X", vec, want)
	}

	// In service: /INT drops, and the slot is held until RETI.
	if d.INTAsserted() {
		t.Error("/INT still asserted after the interrupt was acknowledged")
	}
	d.RETISeen()
	if d.INTAsserted() {
		t.Error("/INT asserted again after RETI with no new request")
	}

	// What RETI is actually for: the slot is released, so the next request can
	// be taken. Without it the device sits in ISR and blocks the chain, and
	// nothing about /INT alone would show that, because /INT is low either way.
	d.Lower(IM2SourceULA)
	d.Raise(IM2SourceULA)
	if !d.INTAsserted() {
		t.Fatal("/INT not asserted for a second request after RETI: the peripheral " +
			"never left its in-service state")
	}
	if _, ok := d.Acknowledge(); !ok {
		t.Error("the second interrupt was not acknowledged")
	}
}

// NR$C0 bits 7:5 are the programmable top of the vector
// (zxnext.vhd:1999). They were stored and never used.
func TestIM2DriverAppliesTheProgrammableVectorBase(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)
	d.SetVectorBase(0xE0) // NR$C0 bits 7:5 all set
	d.SetEnabled(IM2SourceULA, true)
	d.Raise(IM2SourceULA)

	vec, ok := d.Acknowledge()
	if !ok {
		t.Fatal("no device answered")
	}
	if want := byte(0xE0 | IM2SourceULA<<1); vec != want {
		t.Errorf("vector = $%02X, want $%02X", vec, want)
	}
}

// In pulse mode the device FSMs are held in reset (im2_peripheral.vhd:105), so
// nothing arbitrates and nothing answers. NR$C0 bit 0 resets to zero, which is
// why wiring this changes nothing until a guest asks for it.
func TestIM2DriverIsInertInPulseMode(t *testing.T) {
	d := NewIM2Driver()
	d.SetEnabled(IM2SourceULA, true)
	d.Raise(IM2SourceULA)
	if d.INTAsserted() {
		t.Error("/INT asserted in pulse mode")
	}
	if _, ok := d.Acknowledge(); ok {
		t.Error("a device answered the acknowledge cycle in pulse mode")
	}
}

// Index 0 is the highest priority, so a lower index wins the chain even when a
// higher one asked first.
func TestIM2DriverGivesTheLowestIndexTheVector(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)
	d.SetEnabled(IM2SourceULA, true)
	d.SetEnabled(IM2SourceLine, true)
	d.Raise(IM2SourceULA)  // 11
	d.Raise(IM2SourceLine) // 0, higher priority

	vec, ok := d.Acknowledge()
	if !ok {
		t.Fatal("no device answered")
	}
	if want := byte(IM2SourceLine << 1); vec != want {
		t.Errorf("vector = $%02X, want $%02X: index 0 outranks index 11", vec, want)
	}
}

// The request latch is edge-triggered and gated at the edge:
// im2_peripheral.vhd:90-101 forms int_req as i_int_req AND NOT int_req_d, and
// :167-178 latches only on `int_req and i_int_en`. So a source that raises its
// line while its enable bit is clear latches nothing, and setting the enable
// afterwards does NOT pick it up retroactively: the peripheral has to assert
// again.
//
// This is a trap worth knowing about when wiring a source, because the obvious
// order, raise then enable, silently does nothing. It cost this test an hour.
func TestIM2DriverLatchesOnTheRequestEdge(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)

	// Raise while disabled: no edge is latched.
	d.Raise(IM2SourceULA)
	d.SetEnabled(IM2SourceULA, true)
	if d.INTAsserted() {
		t.Error("/INT asserted after enabling a source whose request was already " +
			"high: the latch needs an edge, and there was none")
	}

	// Drop and re-assert with the enable set: now there is an edge.
	d.Lower(IM2SourceULA)
	d.Raise(IM2SourceULA)
	if !d.INTAsserted() {
		t.Error("/INT not asserted after the source re-asserted with its enable set")
	}
}
