package copper

import "testing"

// A caller compositing a scanline needs to know WHICH clock a MOVE's register
// write landed on, because every pixel generated before it must see the old
// layer state and every pixel from there on the new one. The copper is the only
// thing that knows: its write pulse is copper_dout_s, raised inside the MOVE
// branch (device/copper.vhd:104) and cleared on the following clock.

// TestWroteReportsTheMOVEWritePulse pins that Wrote reflects the most recent
// Step only, and is raised by a MOVE and by nothing else.
func TestWroteReportsTheMOVEWritePulse(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	c.WriteData(0x00) // NOOP: MOVE with a zero register field
	c.WriteData(0x00)
	c.WriteData(0x10) // MOVE reg 0x10
	c.WriteData(0xAA) // val 0xAA
	wait := uint16(0x8000) | (uint16(4) << 9) | 100
	c.WriteData(byte(wait >> 8))
	c.WriteData(byte(wait))

	rw := &fakeRegWriter{}
	c.SetRegWriter(rw)
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	if c.Wrote() {
		t.Error("Wrote before any Step; want false")
	}
	c.Step(0, 0, 1) // the NOOP
	if c.Wrote() {
		t.Error("a NOOP raised Wrote; the write pulse is suppressed on the register field alone")
	}
	c.Step(0, 0, 2) // the MOVE
	if !c.Wrote() {
		t.Error("a MOVE did not raise Wrote")
	}
	c.Step(0, 0, 1) // the WAIT, parked: line 0 is not line 100
	if c.Wrote() {
		t.Error("Wrote stayed raised after a Step that wrote nothing; it reports the last Step only")
	}
}

// TestWroteIgnoresTheValueByteOfANOOP pins the register-field-only rule: the
// hardware tests copper_list_data_i(14 downto 8), so MOVE 0 with a non-zero
// value byte is still a NOOP and still raises no pulse.
func TestWroteIgnoresTheValueByteOfANOOP(t *testing.T) {
	c := New()
	c.SetWritePtrLow(0)
	c.WriteData(0x00) // register field 0
	c.WriteData(0xFF) // value byte 0xFF, which the hardware does not consider
	c.SetRegWriter(&fakeRegWriter{})
	c.SetWritePtrHighAndMode(byte(StartFromZero) << 6)

	c.Step(0, 0, 1)
	if c.Wrote() {
		t.Error("MOVE 0,0xFF raised Wrote; only the 7-bit register field gates the pulse")
	}
}
