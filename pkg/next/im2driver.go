package next

// IM2 peripheral indices on the Next, in daisy-chain priority order: index 0 is
// highest, and the index IS the vector (peripherals.vhd:121). See the map at
// the top of im2.go.
const (
	IM2SourceLine   = 0  // NR$22/$23 raster line interrupt
	IM2SourceUART0R = 1  // uart0 rx
	IM2SourceUART1R = 2  // uart1 rx
	IM2SourceCTC0   = 3  // ctc 0..7 occupy 3..10; only 0..3 are instantiated
	IM2SourceULA    = 11 // ULA frame interrupt
	IM2SourceUART0T = 12 // uart0 tx
	IM2SourceUART1T = 13 // uart1 tx
)

// IM2Driver connects the daisy chain to a CPU that reports bus events rather
// than clocks.
//
// The chain is a per-clock FSM, and driving it every CPU clock is not viable:
// fourteen peripherals at up to 28 MHz is hundreds of millions of state
// evaluations a second. It does not need to be. Every transition in the device
// FSM depends only on its inputs and its latched request, with no counter or
// timer anywhere, so between input changes the machine is a fixed point and
// each state returns itself. Ticking it when an input changes therefore reaches
// exactly the same states as ticking it continuously. What differs is the
// wall-clock moment, and that is already instruction-granular for everything
// else here.
//
// The acknowledge is the one place a single tick is not enough. S0 -> REQ wants
// /M1 high, REQ -> ACK wants M1 and IORQ asserted together, and ACK -> ISR
// wants /M1 high again. Acknowledge plays that sequence out, which is what the
// CPU's own acknowledge cycle does in three T-states.
//
// One signal is genuinely out of reach: o_reti_decode (im2_control.vhd:233) is
// high for the single T-state between the ED prefix and its second byte. No
// instruction-granular CPU can offer it. Nothing in this chain needs it to
// arbitrate, but it is the first thing to check if arbitration comes out wrong.
type IM2Driver struct {
	chain *IM2DaisyChain

	hwIM2      bool // NR$C0 bit 0: 0 = pulse mode, 1 = hardware IM2
	vectorBase byte // NR$C0 bits 7:5
	req        [IM2NumPeriph]bool
	enabled    [IM2NumPeriph]bool
}

// NewIM2Driver returns a driver in pulse mode with every source disabled, which
// is what NR$C0, NR$C4, NR$C5 and NR$C6 all reset to.
func NewIM2Driver() *IM2Driver {
	return &IM2Driver{chain: NewIM2DaisyChain()}
}

// SetMode applies NR$C0 bit 0. False is pulse mode, where im2_peripheral.vhd:105
// holds every device FSM in reset, so the chain arbitrates nothing.
func (d *IM2Driver) SetMode(hwIM2 bool) {
	d.hwIM2 = hwIM2
	d.settle()
}

// SetVectorBase applies NR$C0 bits 7:5, the programmable top of the vector byte.
func (d *IM2Driver) SetVectorBase(nrC0 byte) { d.vectorBase = nrC0 & 0xE0 }

// SetEnabled applies one source's interrupt-enable bit, from NR$C4, NR$C5 or
// NR$C6 depending on the source.
func (d *IM2Driver) SetEnabled(source int, on bool) {
	if source < 0 || source >= IM2NumPeriph {
		return
	}
	d.enabled[source] = on
	d.settle()
}

// Raise and Lower drive one peripheral's request line.
func (d *IM2Driver) Raise(source int) { d.setRequest(source, true) }

// Lower drops it again.
func (d *IM2Driver) Lower(source int) { d.setRequest(source, false) }

func (d *IM2Driver) setRequest(source int, on bool) {
	if source < 0 || source >= IM2NumPeriph {
		return
	}
	d.req[source] = on
	d.settle()
}

// INTAsserted reports whether the chain is pulling /INT low.
func (d *IM2Driver) INTAsserted() bool { return d.chain.IntAsserted() }

// Acknowledge is the CPU's interrupt acknowledge cycle: it plays out the
// M1/IORQ sequence the device FSM needs and returns the winning peripheral's
// vector byte, or false if nothing was requesting.
func (d *IM2Driver) Acknowledge() (byte, bool) {
	if !d.chain.IntAsserted() {
		return 0, false
	}
	// M1 and IORQ together: the winner moves REQ -> ACK and drives the bus.
	d.tick(func(in *IM2Inputs) { in.M1, in.IORQ = true, true })
	vec := d.chain.VectorByte(d.vectorBase)
	// /M1 high again: ACK -> ISR, and the slot is held until RETI.
	d.settle()
	return vec, true
}

// RETISeen is the end-of-interrupt: the peripheral in service releases its slot
// and lets a lower-priority one through.
func (d *IM2Driver) RETISeen() {
	d.tick(func(in *IM2Inputs) { in.RetiSeen = true })
	d.settle()
}

// settle runs the chain to its fixed point with the request lines as they stand
// and no bus event.
//
// One tick is not enough, and that is the whole reason this is exact rather
// than approximate. The device FSM advances one state per clock and the
// request latch is a clock behind it, so a newly enabled source needs several
// clocks before /INT is asserted. On hardware those clocks pass anyway. Here
// the ticks are run until the outputs stop moving, which is the same state the
// hardware reaches, just without waiting for the wall clock.
//
// The bound is a backstop, not a limit: with four device states, a
// combinational IEI chain and no timers, the machine cannot take more than a
// few ticks to stabilise.
func (d *IM2Driver) settle() {
	const maxTicks = 16
	prevInt, prevVec, prevStatus := d.chain.IntN(), d.chain.Vector(), d.chain.StatusBits()
	for i := 0; i < maxTicks; i++ {
		d.tick(nil)
		in, vec, st := d.chain.IntN(), d.chain.Vector(), d.chain.StatusBits()
		if in == prevInt && vec == prevVec && st == prevStatus && i > 0 {
			return
		}
		prevInt, prevVec, prevStatus = in, vec, st
	}
}

func (d *IM2Driver) tick(adjust func(*IM2Inputs)) {
	in := IM2Inputs{
		IM2:    d.hwIM2,
		HWIM2:  d.hwIM2,
		IntReq: d.req,
		IntEn:  d.enabled,
	}
	if adjust != nil {
		adjust(&in)
	}
	d.chain.Tick(in)
}
