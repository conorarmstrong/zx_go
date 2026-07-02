package if1

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

func TestDecodePort(t *testing.T) {
	cases := []struct {
		port uint16
		want portClass
	}{
		{0xE7, portMDR},     // canonical MDR
		{0x00E7, portMDR},   // same with explicit zero high byte
		{0xEF, portCTR},     // canonical CTR
		{0xF7, portNET},     // canonical NET
		{0xFE, portUnknown}, // ULA port — must NOT decode as IF1
		{0x00, portMDR},     // matches MDR mask exactly
		{0x18, portUnknown}, // bits 3 and 4 set — neither pattern
	}
	for _, tc := range cases {
		got := decodePort(tc.port)
		if got != tc.want {
			t.Errorf("decodePort(%04X) = %d, want %d", tc.port, got, tc.want)
		}
	}
}

func TestULAReadWriteUnknownPortFallsThrough(t *testing.T) {
	u := NewULA(nil)
	if _, ok := u.HandlePortRead(0xFE); ok {
		t.Error("HandlePortRead(0xFE): IF1 claimed it, want fall-through")
	}
	if u.HandlePortWrite(0xFE, 0xAA) {
		t.Error("HandlePortWrite(0xFE): IF1 claimed it, want fall-through")
	}
}

// TestULAMotorSelectSequence verifies the bit-bang sequence the IF1
// ROM uses to select drive 1: drive a 1 onto the data line, then
// pulse the clock with a high-to-low edge.
func TestULAMotorSelectSequence(t *testing.T) {
	u := NewULA(nil)
	for i := 0; i < NumDrives; i++ {
		u.Bus.Insert(i, microdrive.New(180))
	}

	// Initial state: clock high, data high (the IF1 ROM idles like this).
	if !u.HandlePortWrite(PortCTR, 0x03) { // bits 0 and 1 high
		t.Fatal("CTR write: not handled")
	}

	// Now drop both clock and data → falling edge with dta=0 → select drive 0.
	if !u.HandlePortWrite(PortCTR, 0x00) {
		t.Fatal("CTR write 2: not handled")
	}
	if !u.Bus.Drive(0).MotorOn {
		t.Errorf("after motor select pulse: drive 0 motor off (want on)")
	}
}

// TestULACTRReadCombinesBusAndStub confirms that DTR/BUSY are forced
// low (peripherals stubbed off) regardless of what the bus reports.
func TestULACTRReadCombinesBusAndStub(t *testing.T) {
	u := NewULA(nil)
	val, ok := u.HandlePortRead(PortCTR)
	if !ok {
		t.Fatal("CTR read: not handled")
	}
	if val&0x08 != 0 {
		t.Errorf("CTR read: DTR bit set (got %02X), want clear (no serial)", val)
	}
	if val&0x10 != 0 {
		t.Errorf("CTR read: BUSY bit set (got %02X), want clear (no SinclairNET)", val)
	}
}

// TestULAMDRReadAfterMotorSelect ties together the motor-select
// sequence with a follow-up MDR data read: select drive 0, then
// read a byte from the cartridge that's now under the head.
func TestULAMDRReadAfterMotorSelect(t *testing.T) {
	u := NewULA(nil)
	c := microdrive.New(180)
	c.SetDataAt(0, 0x42) // sentinel byte at the start of the tape
	u.Bus.Insert(0, c)

	// Pulse the motor-select to engage drive 0 (clock falling edge with
	// dta low → motorOn=true).
	u.HandlePortWrite(PortCTR, 0x03) // clock high, dta high
	u.HandlePortWrite(PortCTR, 0x00) // clock low, dta low → falling edge

	val, _ := u.HandlePortRead(PortMDR) // MDR data
	if val != 0x42 {
		t.Errorf("MDR read after motor select: got %02X, want 0x42", val)
	}
}

// TestULANetReadStubbed checks the NET port's idle read value with no
// serial peripherals plugged in. Both TX and NET are open-collector
// wires that rest low when nothing is driving them (schematic: TX/NET
// are read straight, not inverted, and their floating level is ~0V),
// so bits 7 and 0 read back low — only the six unused bits are high.
func TestULANetReadStubbed(t *testing.T) {
	u := NewULA(nil)
	val, ok := u.HandlePortRead(PortNET)
	if !ok {
		t.Fatal("NET read: not handled")
	}
	if val != 0x7E {
		t.Errorf("NET read with nothing plugged: got %02X, want 0x7E (TX/NET idle-low)", val)
	}
}

// TestULA_writeNET — write path to NET port is a no-op stub
// (RS-232 / SinclairNET have no peripherals connected).
func TestULA_writeNET(t *testing.T) {
	u := NewULA(nil)
	// Write a few patterns — must not panic and must not affect state.
	for _, v := range []byte{0x00, 0xFF, 0x55, 0xAA, 0x03} {
		u.HandlePortWrite(PortNET, v)
	}
	// Read back — still no peripheral, same idle-low TX/NET bits.
	val, _ := u.HandlePortRead(PortNET)
	if val != 0x7E {
		t.Errorf("after writeNET sweep: read = %02X, want 0x7E", val)
	}
}

// TestULAResetClearsState confirms Reset clears the captured comms
// signals and the bus state.
func TestULAResetClearsState(t *testing.T) {
	u := NewULA(nil)
	u.Bus.Insert(0, microdrive.New(180))
	u.HandlePortWrite(PortCTR, 0x03)
	u.HandlePortWrite(PortCTR, 0x00)
	if !u.Bus.Drive(0).MotorOn {
		t.Fatal("setup failed: motor not on")
	}
	u.Reset()
	if u.Bus.Drive(0).MotorOn {
		t.Errorf("after Reset: motor still on")
	}
	if u.commsClkHigh {
		t.Errorf("after Reset: commsClkHigh still set")
	}
}
