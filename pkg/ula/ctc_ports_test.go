package ula

import "testing"

// ctcSpy stands in for the Next's CTC bank so this package can pin the port
// decode without importing the device.
type ctcSpy struct {
	writes []struct {
		addr uint16
		val  byte
	}
	readAt []uint16
	ret    byte
}

func (c *ctcSpy) WritePort(addr uint16, val byte) bool {
	if addr&0xFF != 0x3B || addr>>8 < 0x18 || addr>>8 > 0x1F {
		return false
	}
	c.writes = append(c.writes, struct {
		addr uint16
		val  byte
	}{addr, val})
	return true
}

func (c *ctcSpy) ReadPort(addr uint16) (byte, bool) {
	if addr&0xFF != 0x3B || addr>>8 < 0x18 || addr>>8 > 0x1F {
		return 0, false
	}
	c.readAt = append(c.readAt, addr)
	return c.ret, true
}

func (c *ctcSpy) Tick() {}

// The Next's eight CTC channels sit at $183B..$1F3B (zxnext.vhd:2690). Until
// they were routed here the device existed, was pinned against the FPGA VHDL,
// and could not be reached by any program: a write to $183B fell through to the
// ULA's ordinary dispatch and did nothing.
func TestCTCPortsReachTheDevice(t *testing.T) {
	u := newULAForPortTest(t)
	spy := &ctcSpy{ret: 0x2A}
	u.SetNextCTC(spy)

	for ch := 0; ch < 8; ch++ {
		u.WritePort(uint16(0x183B+ch<<8), byte(0xA0+ch))
	}
	if len(spy.writes) != 8 {
		t.Fatalf("CTC saw %d of 8 port writes, want all eight", len(spy.writes))
	}
	for ch, w := range spy.writes {
		if want := uint16(0x183B + ch<<8); w.addr != want {
			t.Errorf("write %d went to $%04X, want $%04X", ch, w.addr, want)
		}
	}

	if got, _ := u.ReadPort(0x1A3B); got != 0x2A {
		t.Errorf("read of $1A3B = $%02X, want the channel's counter $2A", got)
	}
}

// And it must claim only its own ports. $243B and $253B are the NextReg pair
// and share the low byte, so a decode that keyed on $3B alone would swallow
// them and take the whole register file down with it.
func TestCTCDoesNotClaimNeighbouringPorts(t *testing.T) {
	u := newULAForPortTest(t)
	spy := &ctcSpy{}
	u.SetNextCTC(spy)

	for _, addr := range []uint16{0x173B, 0x203B, 0x243B, 0x253B, 0x1800} {
		u.WritePort(addr, 0x55)
	}
	if len(spy.writes) != 0 {
		t.Errorf("the CTC claimed %d port(s) outside $183B..$1F3B: %+v",
			len(spy.writes), spy.writes)
	}
}
