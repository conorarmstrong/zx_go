package ula

import "testing"

// TestNextRegSelectPortReadBack — reading port $243B returns the
// currently-selected register number (zxnext.vhd:4603
// `port_243b_dat <= nr_register`). NextZXOS's IM1 handler saves the
// guest's selection with `IN` from $243B on entry and restores it
// with `OUT (C),L` at $2040 on exit, so a write-only $243B (floating
// $FF into that save) would corrupt the guest's NR-select on every
// interrupt.
func TestNextRegSelectPortReadBack(t *testing.T) {
	u := newTestULA(t)
	f := &fakeNextRegs{}
	u.SetNextRegs(f)

	u.WritePort(0x243B, 0x57)
	if got, ok := u.ReadPort(0x243B); !ok || got != 0x57 {
		t.Errorf("IN $243B = %#02x (handled=%v), want 0x57 handled", got, ok)
	}
	u.WritePort(0x243B, 0x8E)
	if got, ok := u.ReadPort(0x243B); !ok || got != 0x8E {
		t.Errorf("IN $243B after reselect = %#02x (handled=%v), want 0x8E", got, ok)
	}
}
