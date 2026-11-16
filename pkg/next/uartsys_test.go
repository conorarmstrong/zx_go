package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// newUARTRig builds the UART system on a bare dispatcher and IM2 driver.
func newUARTRig(t *testing.T) (*UARTSystem, *nextregs.Dispatcher, *IM2Driver) {
	t.Helper()
	d := nextregs.New()
	d.Store(0x83, 0xFF)
	im2 := NewIM2Driver()
	WireInterruptEnable2(d)
	WireInterruptControl(d, nil)
	WireInterruptEnable1(d)
	WireInterruptEnable0(d)
	WireIM2(d, im2, nil)
	sys := NewUARTSystem(d, im2, func() int { return 1 })
	return sys, d, im2
}

const (
	portTx     = 0x133B // Tx data / status
	portRx     = 0x143B // Rx data / prescaler
	portSelect = 0x153B
	portFrame  = 0x163B
)

func out(s *UARTSystem, port uint16, v byte) {
	if !s.WritePort(port, v) {
		panic("port not claimed")
	}
	s.Advance(11, 1) // an OUT is eleven T-states
}

func in(s *UARTSystem, port uint16) byte {
	v, ok := s.ReadPort(port)
	if !ok {
		panic("port not claimed")
	}
	s.Advance(11, 1)
	return v
}

// The decode is zxnext.vhd:2639: A15..A11 = 00010, A10 xor (A9 and A8), and a
// low byte of $3B. Four ports are claimed; $173B (A10, A9 and A8 all set) is
// not, nor is anything with another low byte.
func TestUARTPortDecode(t *testing.T) {
	s, _, _ := newUARTRig(t)
	claimed := []uint16{0x133B, 0x143B, 0x153B, 0x163B}
	for _, p := range claimed {
		if _, ok := s.ReadPort(p); !ok {
			t.Errorf("port %04X not claimed", p)
		}
		s.Advance(11, 1)
	}
	for _, p := range []uint16{0x173B, 0x123B, 0x103B, 0x1B3B, 0x233B, 0x033B, 0x143A, 0x143C, 0x14FF, 0x1C3B, 0x183B} {
		if _, ok := s.ReadPort(p); ok {
			t.Errorf("port %04X claimed, want not", p)
		}
		if s.WritePort(p, 0) {
			t.Errorf("port %04X write claimed, want not", p)
		}
	}
}

// The select register's read-back and the power-up defaults, through ports.
func TestUARTRegistersThroughPorts(t *testing.T) {
	s, _, _ := newUARTRig(t)
	if got := in(s, portFrame); got != 0x18 {
		t.Errorf("frame at power-up = %02X, want 18", got)
	}
	if got := in(s, portTx); got != 0x10 {
		t.Errorf("status at power-up = %02X, want 10 (tx empty)", got)
	}
	out(s, portSelect, 0x40|0x10|0x05) // uart 1, msb 5
	if got := in(s, portSelect); got != 0x45 {
		t.Errorf("select read-back = %02X, want 45", got)
	}
}

// NR$83 bit 4 is internal_port_enable(12), the gate on the UART ports
// (zxnext.vhd:2420, :2639). Cleared, the ports are not claimed.
func TestUARTPortsFollowNR83Bit4(t *testing.T) {
	s, d, _ := newUARTRig(t)
	d.Store(0x83, 0xEF)
	if _, ok := s.ReadPort(portTx); ok {
		t.Error("port claimed with NR$83 bit 4 clear")
	}
	if s.WritePort(portTx, 0) {
		t.Error("port write claimed with NR$83 bit 4 clear")
	}
	d.Store(0x83, 0xFF)
	if _, ok := s.ReadPort(portTx); !ok {
		t.Error("port not claimed with NR$83 bit 4 set")
	}
}

// The ESP behind UART 0 answers an AT probe through the ports, in real time
// at 115200 baud: bytes out, wait, bytes back, nothing faked.
func TestATProbeThroughThePorts(t *testing.T) {
	s, _, _ := newUARTRig(t)
	for _, c := range []byte("AT\r") {
		out(s, portTx, c)
	}
	s.Advance(60_000*8/8, 1) // T-states at 3.5 MHz: 60k * 8 = 480k 28 MHz clocks
	var got []byte
	for in(s, portTx)&0x01 != 0 {
		got = append(got, in(s, portRx))
	}
	if string(got) != "OK\r\n" {
		t.Errorf("AT reply %q, want %q", got, "OK\r\n")
	}
}

// Nothing is spent on an idle UART: a long idle stretch costs a handful of
// clocks, however many T-states it spans.
func TestIdleUARTCostsNothing(t *testing.T) {
	s, _, _ := newUARTRig(t)
	s.Advance(1000, 1) // settle
	before := s.Block.Executed()
	s.Advance(100_000_000, 1)
	if ran := s.Block.Executed() - before; ran > 16 {
		t.Errorf("an idle UART executed %d clocks over 100M T-states", ran)
	}
}

// A guest that enables the UART 0 Tx-empty interrupt gets it from the IM2
// chain: NR$C6 bit 2 enables source 12, the chain asserts /INT, and the
// vector is the source's index. Hardware IM2 mode is NR$C0 bit 0.
//
// The request is an edge, as im2_peripheral.vhd:100 has it (int_req is
// i_int_req and not int_req_d), so enabling the interrupt while the Tx FIFO is
// already empty raises nothing: the FIFO has to fill and empty again. Writing
// a byte does that, because the transmitter takes it from the FIFO within a
// few clocks.
func TestUARTTxEmptyRaisesAnIM2Interrupt(t *testing.T) {
	s, d, im2 := newUARTRig(t)
	d.WriteReg(0xC0, 0x01) // hardware IM2
	d.WriteReg(0xC6, 0x04) // uart0 tx empty enable
	s.Advance(100, 1)
	if im2.INTAsserted() {
		t.Fatal("/INT asserted by a level that was already high when the interrupt was enabled")
	}
	out(s, portTx, 0x55)
	if !im2.INTAsserted() {
		t.Fatal("Tx FIFO filled and emptied, /INT not asserted")
	}
	vec, ok := im2.Acknowledge()
	if !ok || vec != IM2SourceUART0T<<1 {
		t.Errorf("vector %02X ok=%v, want %02X", vec, ok, IM2SourceUART0T<<1)
	}
}

// With the interrupt disabled the same traffic raises nothing.
func TestUARTTxEmptyInterruptNeedsItsEnable(t *testing.T) {
	s, d, im2 := newUARTRig(t)
	d.WriteReg(0xC0, 0x01)
	out(s, portTx, 0x55)
	s.Advance(100, 1)
	if im2.INTAsserted() {
		t.Error("/INT asserted with NR$C6 bit 2 clear")
	}
}

// The Rx interrupt follows NR$C6 bits 1:0 as uart.vhd's request equation
// has it (zxnext.vhd:1942): avail raises it unless bit 1 selects the
// three-quarters-full condition only.
func TestUARTRxInterruptModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c6    byte
		fires bool
	}{
		{"avail (bit 0)", 0x01, true},
		{"near-full only (bit 1)", 0x02, false},
		{"disabled", 0x00, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, im2 := newUARTRig(t)
			d.WriteReg(0xC0, 0x01)
			d.WriteReg(0xC6, tc.c6)
			// Loopback is not reachable from the machine; the ESP provides
			// the byte: AT\r makes it answer.
			for _, c := range []byte("AT\r") {
				out(s, portTx, c)
			}
			s.Advance(500_000, 1)
			if got := im2.INTAsserted(); got != tc.fires {
				t.Errorf("INT asserted = %v, want %v", got, tc.fires)
			}
			if tc.fires {
				if vec, _ := im2.Acknowledge(); vec != IM2SourceUART0R<<1 {
					t.Errorf("vector %02X, want %02X", vec, IM2SourceUART0R<<1)
				}
			}
		})
	}
}

// A soft reset clears the FIFOs and the select register but leaves the baud
// and frame registers, because zxnext.vhd ties i_reset_hard low.
func TestSoftResetKeepsBaudAndFrame(t *testing.T) {
	s, d, _ := newUARTRig(t)
	out(s, portFrame, 0x1C)
	out(s, portSelect, 0x40)
	out(s, portTx, 0x55)
	d.WriteReg(0x02, 0x01) // soft reset
	if got := in(s, portFrame); got != 0x18 && got != 0x1C {
		t.Fatalf("frame after reset = %02X", got)
	}
	if got := in(s, portSelect); got&0x40 != 0 {
		t.Errorf("select after reset = %02X, want uart 0", got)
	}
	// uart 0 frame is still the power-up value; uart 1's 0x1C survived.
	out(s, portSelect, 0x40)
	if got := in(s, portFrame); got != 0x18 {
		t.Errorf("uart 1 frame = %02X, want 18", got)
	}
}

// Capture and restore round-trips the system, in-flight traffic included.
func TestUARTSystemStateRoundTrip(t *testing.T) {
	a, _, _ := newUARTRig(t)
	for _, c := range []byte("AT\r") {
		out(a, portTx, c)
	}
	a.Advance(12_000, 1)
	b, _, _ := newUARTRig(t)
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	a.Advance(500_000, 1)
	b.Advance(500_000, 1)
	for i := 0; i < 8; i++ {
		x, y := in(a, portTx), in(b, portTx)
		if x != y {
			t.Fatalf("status %02X vs %02X", x, y)
		}
		if x&1 != 0 {
			if p, q := in(a, portRx), in(b, portRx); p != q {
				t.Fatalf("data %02X vs %02X", p, q)
			}
		}
	}
}

// Time is accounted once. An access spends its strobe clocks, two T-states of
// the CPU clock, and the instruction's Advance only owes the rest, so an IN or
// OUT costs exactly its T-states at every speed. A transmitter in flight keeps
// the block from skipping, which makes the executed count the elapsed count.
func TestAccessesAreChargedAgainstTheirInstruction(t *testing.T) {
	for _, speed := range []int{1, 2, 4, 8} {
		s, _, _ := newUARTRig(t)
		sp := speed
		s.speed = func() int { return sp }
		out(s, portTx, 0x55) // keep the transmitter busy for the next 2,400 clocks
		before := s.Block.Executed()
		if _, ok := s.ReadPort(portTx); !ok {
			t.Fatal("port not claimed")
		}
		s.Advance(11, speed)
		if got, want := s.Block.Executed()-before, uint64(11*8/speed); got != want {
			t.Errorf("speed x%d: an IN spent %d clocks, want %d", speed, got, want)
		}
		before = s.Block.Executed()
		s.Advance(23, speed)
		if got, want := s.Block.Executed()-before, uint64(23*8/speed); got != want {
			t.Errorf("speed x%d: 23 T-states advanced %d clocks, want %d", speed, got, want)
		}
	}
}

// Both reset triggers reach the block: NR$02 bit 0 is a soft reset and bit 1 a
// hard one, and i_reset_hard is tied low so each is only i_reset to the UART.
func TestBothNR02ResetsReachTheUART(t *testing.T) {
	for _, v := range []byte{0x01, 0x02} {
		s, d, _ := newUARTRig(t)
		out(s, portSelect, 0x40)
		d.WriteReg(0x02, v)
		if got := in(s, portSelect); got&0x40 != 0 {
			t.Errorf("NR$02=%02X left UART 1 selected (%02X)", v, got)
		}
	}
}

// UART 1's Rx request has its own NR$C6 bits (5:4), and the avail term drops
// out of it with bit 5, as UART 0's does with bit 1. There is no module behind
// UART 1, so the byte comes from its loopback.
func TestUART1RxInterruptModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c6    byte
		fires bool
	}{
		{"avail (bit 4)", 0x10, true},
		{"near-full only (bit 5)", 0x20, false},
		{"uart 0's bit 1 does not count", 0x02, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, im2 := newUARTRig(t)
			s.Block.SetLoopback(1, true)
			d.WriteReg(0xC0, 0x01)
			d.WriteReg(0xC6, tc.c6)
			out(s, portSelect, 0x40)
			out(s, portTx, 0x42)
			s.Advance(40_000, 1)
			if got := im2.INTAsserted(); got != tc.fires {
				t.Errorf("INT asserted = %v, want %v", got, tc.fires)
			}
			if tc.fires {
				if vec, _ := im2.Acknowledge(); vec != IM2SourceUART1R<<1 {
					t.Errorf("vector %02X, want %02X", vec, IM2SourceUART1R<<1)
				}
			}
		})
	}
}

// The request level follows NR$C6 bit 1, not only the FIFO: masking the avail
// term drops the level, and unmasking it raises it again, which is a fresh
// edge for the chain. A write to NR$C6 therefore has to re-derive the lines.
func TestNRC6WriteRederivesTheRxRequest(t *testing.T) {
	s, d, im2 := newUARTRig(t)
	d.WriteReg(0xC0, 0x01)
	d.WriteReg(0xC6, 0x01)
	for _, c := range []byte("AT\r") {
		out(s, portTx, c)
	}
	s.Advance(500_000, 1)
	if !im2.INTAsserted() {
		t.Fatal("setup: the ESP's reply did not raise the Rx interrupt")
	}
	im2.Acknowledge()
	im2.RETISeen()
	if im2.INTAsserted() {
		t.Fatal("setup: interrupt still asserted after service")
	}
	d.WriteReg(0xC6, 0x02) // avail masked: the level drops
	d.WriteReg(0xC6, 0x01) // unmasked: the level rises, a new edge
	if !im2.INTAsserted() {
		t.Error("re-enabling the avail term while a byte waits raised no new interrupt")
	}
}

// A capture holds the request levels and the clock debt: restored into a
// fresh system whose chain already has the level, it must not see a new edge,
// and it must charge the same time for the access in flight.
func TestUARTSystemStateKeepsLevelsAndDebt(t *testing.T) {
	a, da, ima := newUARTRig(t)
	b, db, imb := newUARTRig(t)
	for _, d := range []*nextregs.Dispatcher{da, db} {
		d.WriteReg(0xC0, 0x01)
		d.WriteReg(0xC6, 0x04) // tx-empty interrupt, and the line is already high
	}
	out(a, portTx, 0x55)
	a.Advance(4000, 1) // byte sent, FIFO empty, interrupt raised and taken
	ima.Acknowledge()
	ima.RETISeen()
	if _, ok := a.ReadPort(portTx); !ok { // leaves clock debt
		t.Fatal("port not claimed")
	}
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if a.owed != b.owed {
		t.Errorf("clock debt %d, restored %d", a.owed, b.owed)
	}
	// a has a byte's worth of traffic behind it, so its levels differ from the
	// fresh b's, which is what makes this a test of the restore.
	b.req = [4]bool{true, true, false, false}
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if a.req != b.req {
		t.Errorf("request levels %v, restored %v", a.req, b.req)
	}
	// b's chain never saw the byte go out; its levels came from the capture.
	b.Advance(1, 1)
	if imb.INTAsserted() {
		t.Error("restoring a held level raised a new interrupt edge")
	}
}

// An I/O cycle holds the strobe for two T-states of the CPU clock, which is 16
// of the 28 MHz clocks at 3.5 MHz and 2 at 28 MHz. This is the modelling
// choice the register timing rests on, so it is pinned.
func TestAnIOCycleIsTwoCPUTStates(t *testing.T) {
	s, _, _ := newUARTRig(t)
	for speed, want := range map[int]int{1: 16, 2: 8, 4: 4, 8: 2} {
		sp := speed
		s.speed = func() int { return sp }
		if got := s.ioClocks(); got != want {
			t.Errorf("speed x%d: strobe held %d clocks, want %d", speed, got, want)
		}
	}
}

// probeAT sends AT to UART 0 and returns what comes back.
func probeAT(s *UARTSystem) string {
	for _, c := range []byte("AT\r") {
		out(s, portTx, c)
	}
	s.Advance(60_000, 1)
	var got []byte
	for in(s, portTx)&0x01 != 0 {
		got = append(got, in(s, portRx))
	}
	return string(got)
}

// NR$0B with the I/O mode enabled and mode bit 5 set gives a UART to the
// joystick connector (zxnext.vhd:3340-3350). Bit 0 picks UART 0 or UART 1.
// UART 0 loses the ESP: its peer sees an idle line and its Rx is the joystick
// pin, which idles high.
func TestJoystickRoutingCutsTheESPOffUART0(t *testing.T) {
	s, d, _ := newUARTRig(t)
	d.WriteReg(0x0B, 0xA0) // enable, mode bit 5, UART 0
	if got := probeAT(s); got != "" {
		t.Errorf("the ESP answered %q with UART 0 routed to the joystick port", got)
	}
}

func TestJoystickRoutingOfUART1LeavesTheESPAlone(t *testing.T) {
	s, d, _ := newUARTRig(t)
	d.WriteReg(0x0B, 0xA1) // UART 1
	if got := probeAT(s); got != "OK\r\n" {
		t.Errorf("ESP reply %q with UART 1 routed, want OK", got)
	}
}

func TestJoystickRoutingNeedsBothTheEnableAndTheMode(t *testing.T) {
	for _, v := range []byte{0x20, 0x80, 0x90, 0x00} {
		s, d, _ := newUARTRig(t)
		d.WriteReg(0x0B, v)
		if got := probeAT(s); got != "OK\r\n" {
			t.Errorf("NR$0B=$%02X cut the ESP off (reply %q)", v, got)
		}
	}
}

// Routing is undone by writing it off, and by a register reset, which drives
// NR$0B to zero.
func TestJoystickRoutingIsUndone(t *testing.T) {
	for name, undo := range map[string]func(d *nextregs.Dispatcher){
		"write":  func(d *nextregs.Dispatcher) { d.WriteReg(0x0B, 0x00) },
		"reset":  func(d *nextregs.Dispatcher) { d.Reset() },
		"unmode": func(d *nextregs.Dispatcher) { d.WriteReg(0x0B, 0x80) },
	} {
		s, d, _ := newUARTRig(t)
		d.Store(0x83, 0xFF)
		d.WriteReg(0x0B, 0xA0)
		undo(d)
		d.Store(0x83, 0xFF)
		if got := probeAT(s); got != "OK\r\n" {
			t.Errorf("%s: reply %q after undoing the routing, want OK", name, got)
		}
	}
}

// The routing is part of a captured state: restore into a fresh system and
// the ESP stays cut off.
func TestJoystickRoutingSurvivesAStateRoundTrip(t *testing.T) {
	s, d, _ := newUARTRig(t)
	d.WriteReg(0x0B, 0xA0)
	b, d2, _ := newUARTRig(t)
	d2.Store(0x0B, 0xA0)
	if err := b.LoadState(s.SaveState()); err != nil {
		t.Fatal(err)
	}
	if got := probeAT(b); got != "" {
		t.Errorf("restored system let the ESP answer: %q", got)
	}
}

// The routed UART's pins are the joystick buttons (zxnext.vhd:3538-3539): Rx is
// the inverse of button C and CTS_n the inverse of button B, both active-high
// inputs. NR$0B bit 4 picks the connector (0 left, 1 right); bit 0 picks the
// UART. The unrouted UART keeps its own idle lines.
func TestJoystickButtonsDriveTheRoutedUARTPins(t *testing.T) {
	type tc struct {
		nr0b      byte
		connector int
		uart      int
	}
	for _, c := range []tc{{0xA0, 0, 0}, {0xA1, 0, 1}, {0xB0, 1, 0}, {0xB1, 1, 1}} {
		s, d, _ := newUARTRig(t)
		d.WriteReg(0x0B, c.nr0b)
		rx, ctsN := s.Block.Lines(c.uart)
		if !rx || !ctsN {
			t.Errorf("NR$0B=$%02X: idle pins rx=%v cts_n=%v, want both high", c.nr0b, rx, ctsN)
		}
		s.SetJoystickButtons(c.connector, true, true) // B and C pressed
		if rx, ctsN = s.Block.Lines(c.uart); !rx || !ctsN {
			t.Errorf("NR$0B=$%02X: the UI-side press reached the pins before the emulation step", c.nr0b)
		}
		s.Advance(0, 1) // the emulation goroutine applies it
		rx, ctsN = s.Block.Lines(c.uart)
		if rx || ctsN {
			t.Errorf("NR$0B=$%02X: pressed pins rx=%v cts_n=%v, want both low", c.nr0b, rx, ctsN)
		}
		// The other connector's buttons do nothing.
		s.SetJoystickButtons(c.connector, false, false)
		s.SetJoystickButtons(1-c.connector, true, true)
		s.Advance(0, 1)
		if rx, ctsN = s.Block.Lines(c.uart); !rx || !ctsN {
			t.Errorf("NR$0B=$%02X: other connector moved the pins", c.nr0b)
		}
		// The unrouted UART keeps rx high and cts_n low.
		if rx, ctsN = s.Block.Lines(1 - c.uart); !rx || ctsN {
			t.Errorf("NR$0B=$%02X: unrouted UART rx=%v cts_n=%v", c.nr0b, rx, ctsN)
		}
	}
}

// Without routing the buttons reach neither UART.
func TestJoystickButtonsIgnoredWhenNotRouted(t *testing.T) {
	s, _, _ := newUARTRig(t)
	s.SetJoystickButtons(0, true, true)
	s.Advance(0, 1)
	for u := 0; u < 2; u++ {
		if rx, ctsN := s.Block.Lines(u); !rx || ctsN {
			t.Errorf("UART %d: rx=%v cts_n=%v with routing off", u, rx, ctsN)
		}
	}
}

// The UI goroutine presses buttons while the emulation goroutine runs; run
// under -race this must stay quiet.
func TestJoystickButtonsAreSafeFromAnotherGoroutine(t *testing.T) {
	s, d, _ := newUARTRig(t)
	d.WriteReg(0x0B, 0xA1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.SetJoystickButtons(0, i&1 != 0, false)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		s.Advance(4, 1)
	}
	<-done
}
