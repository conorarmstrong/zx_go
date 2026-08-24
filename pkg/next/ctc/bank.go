package ctc

// NumChannels is the number of CTC channels the Next carries: four.
//
// The IM2 daisy chain reserves eight priority slots for the CTC (indices 3 to
// 10, zxnext.vhd:1936) and two earlier instantiations in the source would have
// filled them, but both are commented out. The live one is NUM_CTC => 4
// (zxnext.vhd:4064-4069), and the top four slots are tied off:
// ctc_zc_to(7 downto 4) and ctc_int_en(7 downto 4) are both hardwired to zero
// (:4092-4093). ctc.vhd generates channels only for 0..NUM_CTC-1 (:100-123) and
// its select decode compares against the same bound (:130-136), so a port that
// selects 4..7 matches no channel: the write goes nowhere and the read-back mux
// ORs nothing.
//
// Eight was a misreading of the daisy chain's slot count as a channel count.
const NumChannels = 4

// Port decode, from zxnext.vhd:2690:
//
//	port_ctc <= '1' when cpu_a(15 downto 11) = "00011" and port_3b_lsb = '1' ...
//
// So the low byte is $3B and the top five address bits are 00011, which puts
// the group in the $18xx..$1Fxx pages. The three bits below that, a10..a8,
// select the channel. Only the first four reach one, so $183B is channel 0 up
// to $1B3B for channel 3, and $1C3B..$1F3B decode as the CTC's and select
// nothing. See NumChannels.
const (
	portLow      = 0x3B
	portPageBase = 0x18
	// The decode spans eight pages because it is three address bits wide, but
	// only the first four reach a channel. $1C3B..$1F3B are decoded as the
	// CTC's and select nothing, which is not the same as not being ours: they
	// must not fall through to the ordinary port dispatch.
	portPageLast    = portPageBase + 8 - 1
	portPageChannel = portPageBase + NumChannels - 1
)

// Bank is the Next's group of four CTC channels together with the port decode
// that reaches them.
//
// It exists so the emulator constructs, ticks and routes to one thing. The
// channels were complete and pinned against the FPGA VHDL long before anything
// could reach them, which is exactly the state the reachability invariant in
// ROADMAP's "Key invariants" is about: a
// verified model that no guest could use looked, from the outside, identical to
// a working feature.
type Bank struct {
	ch [NumChannels]*Channel
}

// NewBank returns four hard-reset channels.
func NewBank() *Bank {
	b := &Bank{}
	for i := range b.ch {
		b.ch[i] = New()
	}
	return b
}

// Channel returns one channel by index, for wiring its trigger input or reading
// its interrupt state, and nil for an index that names no channel.
//
// Nil rather than a panic because PortChannel reports -1 for the four decoded
// addresses that select nothing, and the two are meant to be used together.
func (b *Bank) Channel(i int) *Channel {
	if i < 0 || i >= NumChannels {
		return nil
	}
	return b.ch[i]
}

// PortChannel reports which channel a port address selects, and whether the
// address belongs to the CTC at all.
//
// Selecting no channel is still the CTC's address: ok is true with a channel of
// -1 for $1C3B..$1F3B, which the FPGA decodes and then matches against nothing.
func (b *Bank) PortChannel(addr uint16) (int, bool) {
	if addr&0xFF != portLow {
		return 0, false
	}
	page := addr >> 8
	if page < portPageBase || page > portPageLast {
		return 0, false
	}
	if page > portPageChannel {
		return -1, true // decoded, selects no channel
	}
	return int(page - portPageBase), true
}

// WritePort delivers a control word or time constant to the addressed channel.
// Reports whether the address was the CTC's.
func (b *Bank) WritePort(addr uint16, val byte) bool {
	i, ok := b.PortChannel(addr)
	if !ok {
		return false
	}
	if i >= 0 {
		b.ch[i].Write(val)
	}
	return true
}

// ReadPort returns the addressed channel's live down-counter, which is what a
// CTC read yields on hardware (ctc_chan.vhd's o_cpu_d is t_count), and whether
// the address was the CTC's at all. The caller needs the second value: a port
// this device does not own must fall through to the ordinary dispatch rather
// than be answered with a plausible-looking byte.
func (b *Bank) ReadPort(addr uint16) (byte, bool) {
	i, ok := b.PortChannel(addr)
	if !ok {
		return 0, false
	}
	if i < 0 {
		// The read-back mux ORs the selected channels' outputs, and none is
		// selected (ctc.vhd:163-175), so the result is zero rather than a
		// floating bus.
		return 0, true
	}
	return b.ch[i].Count(), true
}

// Tick advances every channel by one CTC clock.
func (b *Bank) Tick() {
	for _, c := range b.ch {
		c.Tick()
	}
}
