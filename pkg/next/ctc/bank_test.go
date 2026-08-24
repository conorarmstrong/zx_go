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
