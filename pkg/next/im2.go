package next

// IM2 interrupt daisy-chain controller — a faithful port of the FPGA core's
// interrupt arbitration (device/peripherals.vhd, device/im2_peripheral.vhd,
// device/im2_device.vhd). This models the Z80 IM2 vectored-interrupt scheme
// the Next implements in hardware: NUM_PERIPH interrupting peripherals chained
// by priority, each able to assert /INT, win the INT acknowledge cycle, place
// its vector index on the data bus, hold its in-service slot, and release it on
// reti (end of interrupt).
//
// Priority order (peripherals.vhd:86-128 + zxnext.vhd:1930-1944): index 0 is
// the HIGHEST priority and lower indices are higher priority. The priority
// number IS the vector index (im2_device i_vec = to_unsigned(I-1,VEC_BITS),
// peripherals.vhd:121). On the Next those indices are:
//
//	 0 = line          7..  3 = ctc 0..7 are 3..10
//	 1 = uart0 rx       11 = ula (frame INT)
//	 2 = uart1 rx       12 = uart0 tx
//	 3..10 = ctc 0..7   13 = uart1 tx
//
// The full Z80 IM2 vector byte placed on the bus during INT ack is
// `nr_c0_im2_vector & vector & '0'` (zxnext.vhd:1999): the programmable upper
// bits NR$C0[7:5], the 4-bit daisy-chain vector index, then a low 0.
//
// This is the daisy-chain controller in isolation; it is intentionally
// decoupled from pkg/z80 so the CPU stays peripheral-agnostic. The golden
// vectors that pin it (testdata/im2_golden.txt, replayed by im2_test.go)
// come from simulating the real peripherals.vhd under GHDL.

// im2DevState is the per-peripheral FSM state (im2_device.vhd:83).
type im2DevState int

const (
	im2S0  im2DevState = iota // idle, no request
	im2REQ                    // requesting (drives /INT if at head of chain)
	im2ACK                    // INT acknowledge cycle (presents its vector)
	im2ISR                    // in service (holds slot until reti)
)

// IM2NumPeriph is the Next's peripheral count (zxnext.vhd:1962).
const IM2NumPeriph = 14

// im2Peripheral holds one peripheral's latched-request + sticky-status state
// (im2_peripheral.vhd) wrapping its im2_device FSM (im2_device.vhd).
type im2Peripheral struct {
	// im2_peripheral.vhd request edge-detect + latch
	intReqD   bool // int_req_d  : registered i_int_req (CLK_28) (line 94/96)
	im2IntReq bool // im2_int_req: sticky request fed to the device (line 167-178)
	intStatus bool // int_status : sticky pending record, cleared by status_clear (line 154-163)

	// im2_peripheral.vhd isr-serviced edge detect (line 137-148)
	isrServicedD bool

	// im2_device.vhd FSM
	state im2DevState
}

// IM2DaisyChain models device/peripherals.vhd: NUM_PERIPH peripherals chained
// head (index 0, highest priority) to tail.
type IM2DaisyChain struct {
	per [IM2NumPeriph]im2Peripheral

	// Last sampled combinational outputs (valid after Tick).
	intN   bool // o_int_n  : true = inactive (/INT high) (peripherals.vhd:146-156)
	vector int  // o_vector : OR-ed 4-bit vector index   (peripherals.vhd:134-144)
	ieo    bool // o_ieo    : daisy-chain output         (peripherals.vhd:132)
	status [IM2NumPeriph]bool
}

// IM2Inputs is one tick of stimulus to the daisy chain (CLK_CPU + CLK_28 edge).
type IM2Inputs struct {
	Reset bool // i_reset (synchronous, active high)

	M1   bool // /m1 active   (true => i_m1_n='0')   CPU M1 fetch in progress
	IORQ bool // /iorq active (true => i_iorq_n='0') used with M1 to mark INT ack
	IM2  bool // i_im2_mode: 1 if the Z80 is in IM2 (set true if unknown)

	// hw IM2 mode enable (i_mode_pulse_0_im2_1): true = vectored IM2 mode,
	// false = legacy pulse mode. im2_reset_n = mode & not reset
	// (im2_peripheral.vhd:105) — in pulse mode the device FSM is held reset.
	HWIM2 bool

	RetiDecode bool // i_reti_decode (from im2_control o_reti_decode)
	RetiSeen   bool // i_reti_seen   (from im2_control o_reti_seen) — EOI pulse

	// Per-peripheral request inputs (index 0 = highest priority).
	IntReq [IM2NumPeriph]bool // i_int_req : level request, gated by IntEn
	IntEn  [IM2NumPeriph]bool // i_int_en  : enable for this device
	IntUnq [IM2NumPeriph]bool // i_int_unq : unqualified request (bypasses IntEn)

	StatusClear [IM2NumPeriph]bool // i_int_status_clear : clear sticky status bit
}

// NewIM2DaisyChain returns a reset daisy chain.
func NewIM2DaisyChain() *IM2DaisyChain {
	c := &IM2DaisyChain{}
	c.intN = true // /INT inactive at reset
	c.ieo = true
	return c
}

// Tick advances the daisy chain by one CLK_CPU/CLK_28 edge with the given
// inputs, then recomputes the combinational outputs. This collapses the FPGA's
// two clock domains (CLK_28 request latch + CLK_CPU device FSM) into one tick
// edge, exactly as the golden testbench drives them aligned.
func (c *IM2DaisyChain) Tick(in IM2Inputs) {
	// im2_reset_n = i_mode_pulse_0_im2_1 and not i_reset (im2_peripheral.vhd:105)
	im2ResetN := in.HWIM2 && !in.Reset

	// --- Stage 1: clocked state updates (rising edge), in the FPGA's order. ---
	// All next-state values are computed from the CURRENT state/inputs, then
	// committed, mirroring the synchronous processes.

	// im2_device next state (im2_device.vhd:91-132). The daisy-chain iei into
	// device I is the ieo of device I-1, which depends on each device's CURRENT
	// state, so compute the chain combinationally first.
	iei := c.computeIEIChain(in.RetiDecode)

	type nextSt struct {
		state        im2DevState
		intReqD      bool
		im2IntReq    bool
		intStatus    bool
		isrServicedD bool
	}
	var ns [IM2NumPeriph]nextSt

	for i := range c.per {
		p := &c.per[i]

		// im2_peripheral request edge detect (CLK_28) (line 90-101):
		//   int_req_d <= i_int_req ; int_req <= i_int_req and not int_req_d
		intReqEdge := in.IntReq[i] && !p.intReqD

		// im2_device combinational outputs needed for the latch updates:
		//   o_isr_serviced = '1' when state=S_ISR and state_next=S_0 (line 159)
		devStateNext := c.deviceNextStateIdx(i, in, iei[i])
		isrServiced := p.state == im2ISR && devStateNext == im2S0

		// im2_isr_serviced = isr_serviced and not isr_serviced_d (line 148)
		im2IsrServiced := isrServiced && !p.isrServicedD

		// im2_int_req sticky latch (line 167-178):
		//   reset (im2_reset_n=0) -> 0
		//   elsif i_int_unq=1 or (int_req and i_int_en) -> 1
		//   else im2_int_req and not im2_isr_serviced
		var nextIm2IntReq bool
		switch {
		case !im2ResetN:
			nextIm2IntReq = false
		case in.IntUnq[i] || (intReqEdge && in.IntEn[i]):
			nextIm2IntReq = true
		default:
			nextIm2IntReq = p.im2IntReq && !im2IsrServiced
		}

		// int_status sticky latch (CLK_28) (line 154-163):
		//   reset -> 0
		//   else (int_req or i_int_unq) or (int_status and not status_clear)
		var nextStatus bool
		if in.Reset {
			nextStatus = false
		} else {
			nextStatus = (intReqEdge || in.IntUnq[i]) || (p.intStatus && !in.StatusClear[i])
		}

		// int_req_d and isr_serviced_d are CLK_28 registers cleared by reset
		// (im2_peripheral.vhd:92-98, 137-145).
		ns[i] = nextSt{
			state:        devStateNext,
			intReqD:      !in.Reset && in.IntReq[i],
			im2IntReq:    nextIm2IntReq,
			intStatus:    nextStatus,
			isrServicedD: !in.Reset && isrServiced,
		}
	}

	// Commit the clocked state. The im2_device FSM resets on i_reset_n='0',
	// i.e. when im2_reset_n=0 (im2_device.vhd:94; im2_peripheral.vhd:113).
	for i := range c.per {
		if !im2ResetN {
			c.per[i].state = im2S0
		} else {
			c.per[i].state = ns[i].state
		}
		c.per[i].intReqD = ns[i].intReqD
		c.per[i].im2IntReq = ns[i].im2IntReq
		c.per[i].intStatus = ns[i].intStatus
		c.per[i].isrServicedD = ns[i].isrServicedD
	}

	// --- Stage 2: recompute combinational outputs from the new state. ---
	c.recompute(in)
}

// computeIEIChain returns each device's i_iei (ie(I-1)) given the current
// states. ie(0)=i_iei='1' (zxnext.vhd:1984; peripherals.vhd:82). Each device's
// o_ieo (im2_device.vhd:136-146):
//
//	S_0   -> iei
//	S_REQ -> iei and reti_decode
//	other -> '0'
func (c *IM2DaisyChain) computeIEIChain(retiDecode bool) [IM2NumPeriph]bool {
	var iei [IM2NumPeriph]bool
	prev := true // ie(0) = i_iei = '1'
	for i := range c.per {
		iei[i] = prev
		var ieo bool
		switch c.per[i].state {
		case im2S0:
			ieo = prev
		case im2REQ:
			ieo = prev && retiDecode
		default:
			ieo = false
		}
		prev = ieo
	}
	return iei
}

// recompute sets the combinational outputs (o_int_n, o_vector, o_ieo,
// o_int_status) from current state (peripherals.vhd:130-156 + im2_device.vhd
// :148-159).
func (c *IM2DaisyChain) recompute(in IM2Inputs) {
	iei := c.computeIEIChain(in.RetiDecode)

	intN := true // ANDed; starts inactive
	vector := 0  // ORed
	for i := range c.per {
		p := &c.per[i]
		// o_int_n = '0' when state=S_REQ and iei=1 and im2_mode=1 (line 150)
		if p.state == im2REQ && iei[i] && in.IM2 {
			intN = false
		}
		// o_vec = i_vec when state=S_ACK or state_next=S_ACK (line 155).
		// We need state_next here too.
		next := c.deviceNextStateIdx(i, in, iei[i])
		if p.state == im2ACK || next == im2ACK {
			vector |= i // i_vec = index = priority = vector
		}
		c.status[i] = p.intStatus || p.im2IntReq // o_int_status (line 180)
	}
	c.intN = intN
	c.vector = vector
	// o_ieo = ie(NUM_PERIPH): the ieo emerging from the last device in the
	// chain (peripherals.vhd:132).
	c.ieo = c.tailIEO(in.RetiDecode)
}

// deviceNextStateIdx is deviceNextState but reads the device's latched request
// by index (the real im2_int_req).
func (c *IM2DaisyChain) deviceNextStateIdx(i int, in IM2Inputs, iei bool) im2DevState {
	p := &c.per[i]
	switch p.state {
	case im2S0:
		if p.im2IntReq && !in.M1 {
			return im2REQ
		}
		return im2S0
	case im2REQ:
		if in.M1 && in.IORQ && iei && in.IM2 {
			return im2ACK
		}
		return im2REQ
	case im2ACK:
		if !in.M1 {
			return im2ISR
		}
		return im2ACK
	case im2ISR:
		if in.RetiSeen && iei && in.IM2 {
			return im2S0
		}
		return im2ISR
	default:
		return im2S0
	}
}

// tailIEO returns ie(NUM_PERIPH): the ieo emerging from the last device.
func (c *IM2DaisyChain) tailIEO(retiDecode bool) bool {
	prev := true
	for i := range c.per {
		switch c.per[i].state {
		case im2S0:
			// ieo = iei: prev passes through unchanged
		case im2REQ:
			prev = prev && retiDecode
		default:
			prev = false
		}
	}
	return prev
}

// IntN reports the active-low INT line (true = inactive). /INT is asserted to
// the CPU when this is false.
func (c *IM2DaisyChain) IntN() bool { return c.intN }

// IntAsserted reports whether the daisy chain is asserting the Z80 INT line.
func (c *IM2DaisyChain) IntAsserted() bool { return !c.intN }

// Vector returns the 4-bit daisy-chain vector index currently on the bus
// (valid during the INT ack cycle). Combine with NR$C0[7:5] and a low 0 to
// form the Z80 IM2 vector byte: byte = (nrC0&0xE0) | (vector<<1).
func (c *IM2DaisyChain) Vector() int { return c.vector }

// VectorByte returns the full Z80 IM2 data-bus byte for the current vector:
// `nr_c0_im2_vector & vector & '0'` (zxnext.vhd:1999).
func (c *IM2DaisyChain) VectorByte(nrC0 byte) byte {
	return (nrC0 & 0xE0) | (byte(c.vector) << 1)
}

// IEO returns the daisy-chain output (true = chain open below this controller).
func (c *IM2DaisyChain) IEO() bool { return c.ieo }

// Status returns the per-peripheral sticky interrupt-status bits (o_int_status).
func (c *IM2DaisyChain) Status() [IM2NumPeriph]bool { return c.status }

// StatusBits packs Status into a NUM_PERIPH-bit mask (bit i = peripheral i).
func (c *IM2DaisyChain) StatusBits() uint16 {
	var m uint16
	for i, s := range c.status {
		if s {
			m |= 1 << uint(i)
		}
	}
	return m
}
