package uart

// Hardware-faithful models of the Spectrum Next serial UART PHY, ported from
// the FPGA core RTL (the authoritative oracle):
//
//   uart_tx.vhd  -> TX
//   uart_rx.vhd  -> RX (incl. the debounce noise-rejection front end)
//   fifop.vhd    -> FIFOP (FIFO pointer/flag manager)
//
// These complement the higher-level AT-command ESP stub (UART, above) which
// models the application surface at NextReg 0xA8/0xA9. The types here model the
// bit-level serial PHY and FIFO geometry the real UART registers ($133B etc.)
// expose. They are clocked one i_CLK at a time so the per-clock golden vectors
// captured under GHDL (see fpga_golden_test.go) replay exactly.
//
// Frame byte layout (uart_tx.vhd:34-42, uart_rx.vhd:41-49):
//   bit7 = reset (Tx/Rx -> idle immediately)
//   bit6 = break (Tx) / pause (Rx) while idle
//   bit5 = flow control enable
//   bits4:3 = # data bits (11=8, 10=7, 01=6, 00=5)
//   bit2 = parity enable
//   bit1 = 1 odd parity, 0 even parity
//   bit0 = 0 one stop bit, 1 two stop bits

// ---------------------------------------------------------------------------
// TX : uart_tx.vhd
// ---------------------------------------------------------------------------

type txState int

const (
	txIdle txState = iota
	txRTR
	txStart
	txBits
	txParity
	txStop1
	txStop2
)

// TX is the serial transmitter from uart_tx.vhd. One Clock() call models one
// i_CLK rising edge and returns the serial line level o_Tx for that cycle.
type TX struct {
	// configuration inputs (i_frame / i_prescaler), held by the caller
	frame     byte
	prescaler uint32 // PRESCALER_BITS = 17 in uart.vhd
	ctsN      bool   // i_cts_n: false ('0') = receiver ready
	txEn      bool   // i_Tx_en pulse for the next Clock()
	txByte    byte   // i_Tx_byte

	// registers (uart_tx.vhd signals)
	state         txState
	txShift       byte
	txTimer       uint32
	txBitCount    uint8 // 3-bit
	txParity      bool
	frameParityEn bool // tx_frame_parity_en (latched in IDLE on falling edge)
	frameStopBits bool // tx_frame_stop_bits
	prescLatched  uint32
}

const prescalerBits = 17

// NewTX returns a transmitter reset to idle, with the hardware default frame
// (0x18 = 8N1) and a zero prescaler (the caller must set a real baud divisor).
func NewTX() *TX {
	t := &TX{frame: 0x18}
	t.Reset()
	return t
}

// Reset forces the Tx to idle (i_reset = '1'), matching uart_tx.vhd:166.
func (t *TX) Reset() {
	t.state = txIdle
	t.txShift = 0
	t.txTimer = 0
	t.txBitCount = 0
	t.txParity = false
}

// SetFrame / SetPrescaler hold the configuration inputs.
func (t *TX) SetFrame(f byte)         { t.frame = f }
func (t *TX) SetPrescaler(p uint32)   { t.prescaler = p & ((1 << prescalerBits) - 1) }
func (t *TX) SetCTS(clearToSend bool) { t.ctsN = !clearToSend }

// Send latches a byte and asserts i_Tx_en for exactly the next Clock().
func (t *TX) Send(b byte) {
	t.txByte = b
	t.txEn = true
}

// Busy mirrors o_busy (uart_tx.vhd:234): high unless idle and not in
// break/reset.
func (t *TX) Busy() bool {
	return t.state != txIdle || (t.frame&0x80) != 0 || (t.frame&0x40) != 0
}

// timerExpired: tx_timer(PRESCALER_BITS-1 downto 1) == 0, i.e. timer in {0,1}
// (uart_tx.vhd:131).
func (t *TX) timerExpired() bool { return (t.txTimer >> 1) == 0 }

func (t *TX) bitCountExpired() bool { return t.txBitCount == 0 }

// nextState computes state_next from the combinational process
// (uart_tx.vhd:174-230).
func (t *TX) nextState() txState {
	f := t.frame
	switch t.state {
	case txIdle:
		switch {
		case (f&0x40) != 0 || !t.txEn:
			return txIdle
		case t.txEn && (!t.ctsN || (f&0x20) == 0):
			return txStart
		case t.txEn && (t.ctsN && (f&0x20) != 0):
			return txRTR
		default:
			return txIdle
		}
	case txRTR:
		if t.ctsN && (f&0x20) != 0 {
			return txRTR
		}
		return txStart
	case txStart:
		if t.timerExpired() {
			return txBits
		}
		return txStart
	case txBits:
		switch {
		case !t.bitCountExpired() || !t.timerExpired():
			return txBits
		case t.frameParityEn:
			return txParity
		default:
			return txStop1
		}
	case txParity:
		if t.timerExpired() {
			return txStop1
		}
		return txParity
	case txStop1:
		switch {
		case !t.timerExpired():
			return txStop1
		case t.frameStopBits:
			return txStop2
		default:
			return txIdle
		}
	case txStop2:
		if t.timerExpired() {
			return txIdle
		}
		return txStop2
	default:
		return txIdle
	}
}

// out mirrors the o_Tx combinational output (uart_tx.vhd:236-245).
func (t *TX) out() bool {
	switch t.state {
	case txIdle:
		return (t.frame & 0x40) == 0 // not break
	case txStart:
		return false
	case txBits:
		return (t.txShift & 1) != 0
	case txParity:
		return t.txParity
	default: // RTR, STOP_1, STOP_2
		return true
	}
}

// Clock advances one i_CLK rising edge and returns o_Tx for this cycle.
//
// VHDL register ordering reproduced (all sampled off the SAME clock edge):
//   - falling-edge latch of frame/prescaler params while in IDLE
//     (uart_tx.vhd:105-114) is applied just before this rising edge;
//   - the rising-edge processes (shift, timer, bit-count/parity, state)
//     compute their next values from the CURRENT registers; o_Tx is the
//     combinational output of the current (pre-update) state.
func (t *TX) Clock() bool {
	// The o_Tx level for THIS cycle reflects the current registers.
	level := t.out()

	// --- falling-edge param latch (uart_tx.vhd:105-114): while in IDLE,
	// sample frame parity/stop and the prescaler. Physically this falling
	// edge precedes the rising edge being modelled.
	if t.state == txIdle {
		t.frameParityEn = (t.frame & 0x04) != 0
		t.frameStopBits = (t.frame & 0x01) != 0
		t.prescLatched = t.prescaler
	}

	ns := t.nextState()
	expired := t.timerExpired()

	// Pre-edge register snapshots (VHDL non-blocking: every rising-edge
	// process reads the OLD values).
	curState := t.state
	oldShift := t.txShift
	oldBitCount := t.txBitCount
	oldParity := t.txParity

	// shift register (uart_tx.vhd:118-127)
	switch {
	case curState == txIdle:
		t.txShift = t.txByte
	case curState == txBits && expired:
		t.txShift = oldShift >> 1
	}

	// bit counter & parity (uart_tx.vhd:148-159)
	switch {
	case curState == txIdle:
		// tx_bit_count <= '1' & i_frame(4 downto 3)
		t.txBitCount = uint8(4 | ((t.frame >> 3) & 0x03))
		t.txParity = (t.frame & 0x02) != 0
	case curState == txBits && expired:
		t.txBitCount = (oldBitCount - 1) & 0x07
		t.txParity = oldParity != ((oldShift & 1) != 0)
	}

	// baud timer (uart_tx.vhd:133-142)
	if curState != ns || expired {
		t.txTimer = t.prescLatched
	} else {
		t.txTimer = (t.txTimer - 1) & ((1 << prescalerBits) - 1)
	}

	// state machine (uart_tx.vhd:163-172)
	if (t.frame & 0x80) != 0 { // i_frame(7) reset
		t.state = txIdle
	} else {
		t.state = ns
	}

	// i_Tx_en is a one-cycle pulse consumed by this edge.
	t.txEn = false

	return level
}

// ---------------------------------------------------------------------------
// FIFOP : fifop.vhd  (FIFO pointer/flag manager)
// ---------------------------------------------------------------------------

// FIFOP models fifop.vhd: it tracks the stored-byte count and read/write
// addresses for a depth-2^DEPTH_BITS FIFO, and derives the empty / near-full
// (3/4) / almost-full / full status flags. The real RX FIFO is DEPTH_BITS=9
// (512 bytes), the TX FIFO DEPTH_BITS=6 (64 bytes); the flag equations are
// parameterised identically.
//
// Read and write advance on the FALLING edge of i_rd / i_wr (i.e. the cycle
// after a 1->0 transition) and only when not empty / not full respectively
// (fifop.vhd:79, 105). The overflow guard is the `not full` term on
// wr_advance: a write attempted while full is silently dropped.
type FIFOP struct {
	depthBits uint
	stored    uint32 // DEPTH_BITS+1 wide counter
	rdAddr    uint32
	wrAddr    uint32
	rdDly     bool
	wrDly     bool
	rd        bool
	wr        bool
}

// NewFIFOP returns a reset FIFO-pointer manager with the given depth.
func NewFIFOP(depthBits uint) *FIFOP {
	f := &FIFOP{depthBits: depthBits}
	f.Reset()
	return f
}

// Reset clears the pointers and count (fifop.vhd i_reset paths).
func (f *FIFOP) Reset() {
	f.stored = 0
	f.rdAddr = 0
	f.wrAddr = 0
	f.rdDly = false
	f.wrDly = false
}

// SetRead / SetWrite hold the i_rd / i_wr request lines for the next Clock().
func (f *FIFOP) SetRead(v bool)  { f.rd = v }
func (f *FIFOP) SetWrite(v bool) { f.wr = v }

func (f *FIFOP) full() bool  { return (f.stored>>f.depthBits)&1 != 0 } // stored(DEPTH_BITS)
func (f *FIFOP) empty() bool { return f.stored == 0 }

// addrMask isolates the DEPTH_BITS-wide address.
func (f *FIFOP) addrMask() uint32 { return (1 << f.depthBits) - 1 }

// Clock advances one i_CLK rising edge. The flag/address accessors then read
// the post-edge state, matching the golden which samples after each pulse
// settles.
func (f *FIFOP) Clock() {
	rdAdvance := f.rdDly && !f.rd && !f.empty() // fifop.vhd:79
	wrAdvance := f.wrDly && !f.wr && !f.full()  // fifop.vhd:105 (overflow guard)

	// rd_dly / wr_dly registers (fifop.vhd:68-77, 94-103)
	f.rdDly = f.rd
	f.wrDly = f.wr

	// rd_addr / wr_addr (fifop.vhd:81-90, 107-116)
	if rdAdvance {
		f.rdAddr = (f.rdAddr + 1) & f.addrMask()
	}
	if wrAdvance {
		f.wrAddr = (f.wrAddr + 1) & f.addrMask()
	}

	// stored count (fifop.vhd:120-131)
	storedMask := uint32((uint64(1) << (f.depthBits + 1)) - 1)
	switch {
	case !rdAdvance && wrAdvance:
		f.stored = (f.stored + 1) & storedMask
	case rdAdvance && !wrAdvance:
		f.stored = (f.stored - 1) & storedMask
	}
}

// Empty mirrors o_empty (fifop.vhd:135,140).
func (f *FIFOP) Empty() bool { return f.empty() }

// Full mirrors o_full (fifop.vhd:136,143).
func (f *FIFOP) Full() bool { return f.full() }

// NearFull mirrors o_full_near = stored(DEPTH_BITS) or
// (stored(DEPTH_BITS-1) and stored(DEPTH_BITS-2))  (fifop.vhd:141) — i.e.
// "at least 3/4 full".
func (f *FIFOP) NearFull() bool {
	d := f.depthBits
	top := (f.stored >> d) & 1
	b1 := (f.stored >> (d - 1)) & 1
	b2 := (f.stored >> (d - 2)) & 1
	return top != 0 || (b1 != 0 && b2 != 0)
}

// AlmostFull mirrors o_full_almost (fifop.vhd:142).
//
// HARDWARE QUIRK (faithful): the RTL compares stored(DEPTH_BITS-1 downto 1)
// [DEPTH_BITS-1 bits] against to_unsigned(-1, stored'length-1) [DEPTH_BITS
// bits all-ones]. Under std_logic_unsigned "=" the narrower operand is
// zero-extended to DEPTH_BITS bits, whose top (pad) bit is 0, so it can NEVER
// equal the all-ones constant. The comparison term is therefore always false
// and o_full_almost reduces to exactly stored(DEPTH_BITS) = o_full. Verified
// under GHDL. We reproduce the synthesized behaviour, not the apparent intent.
func (f *FIFOP) AlmostFull() bool { return f.full() }

// ReadAddr / WriteAddr expose o_raddr / o_waddr.
func (f *FIFOP) ReadAddr() uint32  { return f.rdAddr }
func (f *FIFOP) WriteAddr() uint32 { return f.wrAddr }

// ---------------------------------------------------------------------------
// RX : uart_rx.vhd  (+ debounce.vhd noise-rejection front end)
// ---------------------------------------------------------------------------

type rxState int

const (
	rxIdle rxState = iota
	rxStart
	rxBits
	rxParity
	rxStop1
	rxStop2
	rxError
	rxPause
)

// debounce models misc/debounce.vhd with INITIAL_STATE='1', COUNTER_SIZE=N.
// It rejects input pulses narrower than 2^N clocks: button_o only follows
// button_i once the input has been stable for 2^N clocks.
type debounce struct {
	counterSize uint
	button      uint8  // 2-bit shift (button(1) downto button(0))
	counter     uint32 // COUNTER_SIZE+1 wide
	buttonDb    bool
}

func newDebounce(counterSize uint, initial bool) *debounce {
	var b uint8
	var db bool
	if initial {
		b = 0x03
		db = true
	}
	return &debounce{counterSize: counterSize, button: b, buttonDb: db}
}

// clock advances one rising edge with clk_en='1'.
func (d *debounce) clock(in bool) {
	// button <= button(0) & button_i  (debounce.vhd:55)
	inBit := uint8(0)
	if in {
		inBit = 1
	}
	newButton := ((d.button << 1) | inBit) & 0x03
	oldB0 := d.button & 1
	oldB1 := (d.button >> 1) & 1
	buttonNoise := (oldB0 ^ oldB1) != 0 // debounce.vhd:59 uses button(0) xor button(1)

	// output: if counter(COUNTER_SIZE)='1' then button_db <= button(1)
	// (debounce.vhd:76-83) — reads the PRE-edge button(1) and counter.
	top := (d.counter >> d.counterSize) & 1
	if top != 0 {
		d.buttonDb = oldB1 != 0
	}

	// counter (debounce.vhd:63-72)
	if buttonNoise {
		d.counter = 0
	} else if top == 0 {
		d.counter++
	}

	d.button = newButton
}

func (d *debounce) out() bool { return d.buttonDb }

// RX is the serial receiver from uart_rx.vhd. One Clock() call models one
// i_CLK rising edge given the current i_Rx line level.
type RX struct {
	frame     byte
	prescaler uint32

	db  *debounce
	rxD bool // Rx_d register (one-cycle delay of debounced Rx)

	state rxState

	rxShift    byte
	rxTimer    uint32
	rxTimerUpd bool
	rxBitCount uint8
	rxParity   bool

	frameBits     uint8 // rx_frame_bits (2)
	frameParityEn bool
	frameStopBits bool
	prescLatched  uint32

	// outputs (registered)
	rxAvail bool
	errFr   bool
	errPar  bool
}

// NewRX returns a receiver. Per uart_rx.vhd the reset state is S_PAUSE; the
// debounce front end starts high (idle line).
func NewRX() *RX {
	r := &RX{frame: 0x18, db: newDebounce(2, true), rxD: true}
	r.Reset()
	return r
}

// Reset forces S_PAUSE (uart_rx.vhd:219-220).
func (r *RX) Reset() {
	r.state = rxPause
	r.rxAvail = false
	r.errFr = false
	r.errPar = false
}

func (r *RX) SetFrame(f byte)       { r.frame = f }
func (r *RX) SetPrescaler(p uint32) { r.prescaler = p & ((1 << prescalerBits) - 1) }

func (r *RX) timerExpired() bool    { return (r.rxTimer >> 1) == 0 } // uart_rx.vhd:178
func (r *RX) bitCountExpired() bool { return r.rxBitCount == 0 }

// nextState computes state_next (uart_rx.vhd:227-295) using the debounced Rx.
func (r *RX) nextState(rx bool) rxState {
	f := r.frame
	switch r.state {
	case rxIdle:
		switch {
		case (f & 0x40) != 0:
			return rxPause
		case !rx:
			return rxStart
		default:
			return rxIdle
		}
	case rxStart:
		switch {
		case rx:
			return rxIdle
		case r.timerExpired():
			return rxBits
		default:
			return rxStart
		}
	case rxBits:
		switch {
		case !r.bitCountExpired() || !r.timerExpired():
			return rxBits
		case r.frameParityEn:
			return rxParity
		default:
			return rxStop1
		}
	case rxParity:
		switch {
		case !r.timerExpired():
			return rxParity
		case rx == r.rxParity:
			return rxStop1
		default:
			return rxError
		}
	case rxStop1:
		switch {
		case !r.timerExpired():
			return rxStop1
		case !rx:
			return rxError
		case r.frameStopBits:
			return rxStop2
		default:
			return rxIdle
		}
	case rxStop2:
		switch {
		case !r.timerExpired():
			return rxStop2
		case !rx:
			return rxError
		default:
			return rxIdle
		}
	case rxError:
		if !rx {
			return rxError
		}
		return rxIdle
	case rxPause:
		if (f&0x40) != 0 || !rx {
			return rxPause
		}
		return rxIdle
	default:
		return rxIdle
	}
}

// Clock advances one i_CLK rising edge for line level rxIn. After the call,
// Avail()/Byte()/FramingError()/ParityError() report this cycle's outputs.
func (r *RX) Clock(rxIn bool) {
	// --- debounce front end (uart_rx.vhd:119-131). Rx = debounced output.
	// The debounce registers update this edge; the rest of the RX reads the
	// PRE-edge debounced value (the VHDL Rx signal is the debounce's current
	// button_o, stable across this edge until the debounce process commits).
	rx := r.db.out()
	rxD := r.rxD
	rxE := rx != rxD // Rx_e = Rx xor Rx_d (uart_rx.vhd:140)

	// --- falling-edge param latch (uart_rx.vhd:144-154): in S_IDLE, sample
	// frame bits + prescaler. Physically precedes this rising edge.
	if r.state == rxIdle {
		r.frameBits = (r.frame >> 3) & 0x03
		r.frameParityEn = (r.frame & 0x04) != 0
		r.frameStopBits = (r.frame & 0x01) != 0
		r.prescLatched = r.prescaler
	}

	cur := r.state
	ns := r.nextState(rx)
	expired := r.timerExpired()

	oldShift := r.rxShift
	oldBitCount := r.rxBitCount
	oldParity := r.rxParity

	// shift register (uart_rx.vhd:158-174)
	switch {
	case cur == rxStart || ns == rxError:
		r.rxShift = 0xFF
	case (cur == rxBits || cur == rxError) && expired:
		// rx_shift <= Rx & rx_shift(7 downto 1)
		r.rxShift = (oldShift >> 1) | boolBit(rx)<<7
	case cur == rxStop1 && expired:
		switch r.frameBits {
		case 0x02: // "10" = 7 bits
			r.rxShift = oldShift >> 1
		case 0x01: // "01" = 6 bits
			r.rxShift = oldShift >> 2
		case 0x00: // "00" = 5 bits
			r.rxShift = oldShift >> 3
		default: // "11" = 8 bits, no adjust
			r.rxShift = oldShift
		}
	}

	// bit counter & parity (uart_rx.vhd:201-212)
	switch {
	case cur == rxIdle:
		r.rxBitCount = uint8(4 | r.frameBits)
		r.rxParity = (r.frame & 0x02) != 0
	case cur == rxBits && expired:
		r.rxBitCount = (oldBitCount - 1) & 0x07
		r.rxParity = oldParity != rx
	}

	// baud timer (uart_rx.vhd:180-195)
	switch {
	case cur == rxIdle:
		r.rxTimer = r.prescLatched >> 1 // '0' & rx_prescaler(MSB downto 1)
	case expired:
		r.rxTimer = r.prescLatched
		r.rxTimerUpd = false
	case cur != rxStart && rxE && !r.rxTimerUpd:
		r.rxTimer = r.prescLatched >> 1
		r.rxTimerUpd = true
	default:
		r.rxTimer = (r.rxTimer - 1) & ((1 << prescalerBits) - 1)
	}

	// outputs (uart_rx.vhd:299-314), computed from current state + next state
	r.rxAvail = (cur == rxStop1 || cur == rxStop2) && ns == rxIdle
	r.errPar = cur == rxParity && ns == rxError
	r.errFr = (cur == rxStop1 || cur == rxStop2) && ns == rxError

	// Rx_d register (uart_rx.vhd:133-138)
	r.rxD = rx

	// debounce registers commit this edge
	r.db.clock(rxIn)

	// state machine (uart_rx.vhd:216-225)
	if (r.frame & 0x80) != 0 {
		r.state = rxPause
	} else {
		r.state = ns
	}
}

func boolBit(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// Avail mirrors o_Rx_avail (one cycle when a clean byte completes).
func (r *RX) Avail() bool { return r.rxAvail }

// Byte mirrors o_Rx_byte (the recovered data, valid when Avail()).
func (r *RX) Byte() byte { return r.rxShift }

// FramingError / ParityError mirror o_err_framing / o_err_parity (one cycle).
func (r *RX) FramingError() bool { return r.errFr }
func (r *RX) ParityError() bool  { return r.errPar }
