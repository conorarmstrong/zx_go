package ctc

import "testing"

// The Next carries eight CTC channels, decoded at ports $183B..$1F3B:
// zxnext.vhd:2690 gates on cpu_a(15 downto 11) = "00011" with the low byte
// $3B, so the channel is selected by address bits 10..8 and the whole group
// sits in the $18xx..$1Fxx page range.
//
// A Bank is that group. It exists so the emulator has one thing to construct,
// tick and route ports to, rather than eight loose channels and a decode
// rule copied into whichever file needed it.
func TestBankDecodesTheEightCTCPorts(t *testing.T) {
	b := NewBank()

	for ch := 0; ch < NumChannels; ch++ {
		port := uint16(0x183B + ch<<8)
		if got, ok := b.PortChannel(port); !ok || got != ch {
			t.Errorf("port $%04X selected channel %d (ok=%v), want channel %d",
				port, got, ok, ch)
		}
	}

	// Everything outside the group is not ours. $203B is the next page up and
	// $173B the one below; $1800 shares the page but not the low byte.
	for _, port := range []uint16{0x173B, 0x203B, 0x1800, 0x1F3C, 0x243B, 0x253B} {
		if _, ok := b.PortChannel(port); ok {
			t.Errorf("port $%04X was claimed by the CTC bank, which decodes only "+
				"$183B..$1F3B (zxnext.vhd:2690)", port)
		}
	}
}

// A guest programs a channel by writing a control word with D2 set, then the
// time constant. Reading the port returns the live down-counter, which is what
// makes the CTC useful as a timer even before its interrupts are delivered.
func TestBankProgramsAndCountsAChannel(t *testing.T) {
	b := NewBank()
	const port = uint16(0x183B)

	// Control word: D0=1 reset-ish/control, D2=1 "time constant follows",
	// D6=1 counter mode so it counts trigger edges rather than the prescaler.
	b.WritePort(port, 0x05|0x40)
	b.WritePort(port, 3) // time constant

	if got, ok := b.ReadPort(port); !ok || got != 3 {
		t.Fatalf("counter after loading a time constant of 3 = %d, want 3", got)
	}

	// In counter mode the channel counts trigger edges.
	ch, _ := b.PortChannel(port)
	for i := 0; i < 2; i++ {
		b.Channel(ch).SetTrigger(true)
		b.Tick()
		b.Channel(ch).SetTrigger(false)
		b.Tick()
	}
	if got, _ := b.ReadPort(port); got == 3 {
		t.Errorf("counter = %d after two trigger edges, want it to have counted down "+
			"from 3: the bank is not ticking its channels", got)
	}
}

// The Next has four CTC channels, not eight. Two earlier instantiations in
// zxnext.vhd would have given it eight and both are commented out; the live one
// is NUM_CTC => 4 (zxnext.vhd:4064-4069), and the daisy chain's top four slots
// are tied off at :4092-4093. The eight-slot priority map at :1936 is what
// misled this model into eight.
func TestTheNextHasFourChannels(t *testing.T) {
	if NumChannels != 4 {
		t.Errorf("NumChannels = %d, want 4 (zxnext.vhd:4067 NUM_CTC => 4)", NumChannels)
	}
}

// The port decode is three address bits wide, so it spans eight pages, but
// ctc.vhd only generates channels 0..NUM_CTC-1 and its select compares against
// the same bound (ctc.vhd:100-123, :130-136). $1C3B..$1F3B are therefore
// decoded as the CTC's and select nothing: the write goes nowhere, and the
// read-back mux ORs no selected channel and yields zero.
//
// They must still be claimed. Letting them fall through to the ordinary port
// dispatch would answer a decoded CTC port with floating-bus junk.
func TestPortsAboveTheLastChannelAreClaimedButSelectNothing(t *testing.T) {
	b := NewBank()
	for page := uint16(0x1C); page <= 0x1F; page++ {
		addr := page<<8 | 0x3B
		ch, ok := b.PortChannel(addr)
		if !ok {
			t.Errorf("port $%04X is not claimed by the CTC, but the FPGA decodes it", addr)
			continue
		}
		if ch >= 0 {
			t.Errorf("port $%04X selected channel %d, but only 0..%d exist",
				addr, ch, NumChannels-1)
		}
		if !b.WritePort(addr, 0xFF) {
			t.Errorf("a write to $%04X was not claimed", addr)
		}
		if v, ok := b.ReadPort(addr); !ok || v != 0 {
			t.Errorf("read of $%04X = ($%02X, %v), want ($00, true)", addr, v, ok)
		}
	}
	// And a write there must not have reached a real channel.
	for i := 0; i < NumChannels; i++ {
		if got := b.Channel(i).Count(); got != 0 {
			t.Errorf("channel %d counter = $%02X after writes to unselected ports", i, got)
		}
	}
}
