package ula

import "testing"

// fakeI2CBus records SCL/SDA line writes and serves a scripted SDA
// read-back, standing in for pkg/next/rtc.Bus in the port-routing
// tests.
type fakeI2CBus struct {
	scl, sda []bool
	sdaLine  bool
}

func (f *fakeI2CBus) WriteSCL(bit bool) { f.scl = append(f.scl, bit) }
func (f *fakeI2CBus) WriteSDA(bit bool) { f.sda = append(f.sda, bit) }
func (f *fakeI2CBus) ReadSDA() bool     { return f.sdaLine }

// TestI2CPortRouting pins the Next i2c RTC port decode
// (zxnext.vhd:2630-2631 + 3234-3250): OUT $103B latches bit 0 into
// SCL, OUT $113B latches bit 0 into SDA, IN $113B returns the SDA
// line in bit 0 with the upper bits high (open-drain float).
func TestI2CPortRouting(t *testing.T) {
	u := newTestULA(t)
	f := &fakeI2CBus{sdaLine: false}
	u.SetNextI2C(f)

	u.WritePort(0x103B, 0x01)
	u.WritePort(0x103B, 0xFE) // only bit 0 matters → false
	u.WritePort(0x113B, 0x00)
	u.WritePort(0x113B, 0xFF)

	if len(f.scl) != 2 || f.scl[0] != true || f.scl[1] != false {
		t.Errorf("SCL writes = %v, want [true false]", f.scl)
	}
	if len(f.sda) != 2 || f.sda[0] != false || f.sda[1] != true {
		t.Errorf("SDA writes = %v, want [false true]", f.sda)
	}

	if got, ok := u.ReadPort(0x113B); !ok || got != 0xFE {
		t.Errorf("IN $113B with SDA low = %#02x (handled=%v), want 0xFE handled", got, ok)
	}
	f.sdaLine = true
	if got, ok := u.ReadPort(0x113B); !ok || got != 0xFF {
		t.Errorf("IN $113B with SDA high = %#02x (handled=%v), want 0xFF handled", got, ok)
	}
}

// TestI2CPortRoutingNilSafe — without a bus installed the ports fall
// through to the existing dispatch (no panic, classic float read).
func TestI2CPortRoutingNilSafe(t *testing.T) {
	u := newTestULA(t)
	u.WritePort(0x103B, 0x01) // must not panic
	_, _ = u.ReadPort(0x113B)
}
