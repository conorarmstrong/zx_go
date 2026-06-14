package ula

import "testing"

func TestPortTracerFiresOnWriteAndRead(t *testing.T) {
	u := newTestULA(t)
	type call struct {
		addr  uint16
		val   byte
		write bool
		ok    bool
	}
	var calls []call
	u.SetPortTracer(func(addr uint16, val byte, write, handled bool) {
		calls = append(calls, call{addr, val, write, handled})
	})

	// Write to port 0xFE (border colour) — always handled.
	u.WritePort(0x00FE, 0x02)
	// Read keyboard from any FEFE-style address.
	_, _ = u.ReadPort(0x7FFE)

	if len(calls) < 2 {
		t.Fatalf("got %d trace calls, want >= 2", len(calls))
	}
	if calls[0] != (call{0x00FE, 0x02, true, true}) {
		t.Errorf("first call = %+v, want write to $00FE with val=$02", calls[0])
	}
	if !calls[1].ok {
		t.Errorf("read trace marked unhandled; ULA should always handle port reads on classic models")
	}
	if calls[1].write {
		t.Errorf("second call write flag should be false (it was a read)")
	}
}

func TestPortTracerNilIsTransparent(t *testing.T) {
	u := newTestULA(t)
	// Tracer not set — port I/O must still work normally.
	u.WritePort(0x00FE, 0x05)
	if u.BorderColour != 0x05 {
		t.Errorf("BorderColour = %#x after WritePort, want 0x05", u.BorderColour)
	}
}
