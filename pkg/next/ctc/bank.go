package ctc

// NumChannels is the number of CTC channels the Next carries. The FPGA gives
// the IM2 daisy chain eight of them, at priority indices 3 to 10
// (zxnext.vhd:1936).
const NumChannels = 8

// Port decode, from zxnext.vhd:2690:
//
//	port_ctc <= '1' when cpu_a(15 downto 11) = "00011" and port_3b_lsb = '1' ...
//
// So the low byte is $3B and the top five address bits are 00011, which puts
// the group in the $18xx..$1Fxx pages. The three bits below that, a10..a8,
// select the channel, giving $183B for channel 0 up to $1F3B for channel 7.
const (
	portLow      = 0x3B
	portPageBase = 0x18
	portPageLast = portPageBase + NumChannels - 1
)

// Bank is the Next's group of eight CTC channels together with the port decode
// that reaches them.
//
// It exists so the emulator constructs, ticks and routes to one thing. The
// channels were complete and pinned against the FPGA VHDL long before anything
// could reach them, which is exactly the state ROADMAP item 1 is about: a
// verified model that no guest could use looked, from the outside, identical to
// a working feature.
type Bank struct {
	ch [NumChannels]*Channel
}

// NewBank returns eight hard-reset channels.
func NewBank() *Bank {
	b := &Bank{}
	for i := range b.ch {
		b.ch[i] = New()
	}
	return b
}

// Channel returns one channel by index, for wiring its trigger input or reading
// its interrupt state.
func (b *Bank) Channel(i int) *Channel { return b.ch[i] }

// PortChannel reports which channel a port address selects, and whether the
// address belongs to the CTC at all.
func (b *Bank) PortChannel(addr uint16) (int, bool) {
	if addr&0xFF != portLow {
		return 0, false
	}
	page := addr >> 8
	if page < portPageBase || page > portPageLast {
		return 0, false
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
	b.ch[i].Write(val)
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
	return b.ch[i].Count(), true
}

// Tick advances every channel by one CTC clock.
func (b *Bank) Tick() {
	for _, c := range b.ch {
		c.Tick()
	}
}
