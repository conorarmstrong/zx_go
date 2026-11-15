package uart

// Block is the Spectrum Next's UART register block, ported from
// serial/uart.vhd (the oracle). It owns both UARTs, the register map at ports
// $133B (Tx and status), $143B (Rx and prescaler), $153B (select) and $163B
// (frame), the 512-byte Rx and 64-byte Tx FIFOs of each UART, and the status
// and error logic. The serial PHY it drives is TX/RX/FIFOP from serial.go.
//
// Block is clocked one i_CLK (28 MHz) at a time, so the golden captured under
// GHDL replays exactly. It has no mid-cycle phase of its own except one: the
// Tx FIFO memory is written on the falling edge of i_CLK (uart.vhd drives it
// with i_CLK_n), so Clock applies that write first, then the rising edge.
//
// Pins are set with the Set* methods before Clock and read back after it.
// There is no i_reset_hard: zxnext.vhd ties it low (zxnext.vhd:3361), so the
// framing and prescaler registers keep their power-up values across a soft
// reset, and a machine hard reset is a new Block.
type Block struct {
	// pins
	reset  bool
	reg    uint8 // i_uart_reg
	rd, wr bool
	dIn    byte
	rxDrv  [2]bool // line driven by the outside world
	loop   [2]bool // o_Tx fed straight back into i_Rx
	ctsN   [2]bool // i_Tx_x_cts_n

	// register block (uart.vhd:244-376)
	selectR  bool
	prescMSB [2]uint8  // 3 bits
	prescLSB [2]uint16 // 14 bits
	framing  [2]byte

	rx     [2]*RX
	tx     [2]*TX
	rxFifo [2]*FIFOP
	txFifo [2]*FIFOP
	rxMem  [2][512]uint16 // 9 bits: error flag in bit 8
	txMem  [2][64]uint16
	rxO    [2]uint16 // tdpram data output, registered

	txRdD, txWrD [2]bool

	statusOverflow [2]bool
	statusFraming  [2]bool

	rxAvailD [2]bool
	rxByteD  [2]uint16

	// registered outputs
	oTx      [2]bool
	oRtrN    [2]bool
	oRxNear  [2]bool
	oRxAvail [2]bool
	oRxErr   [2]bool
	oRxBreak [2]bool
	oTxEmpty [2]bool

	peer     Peer // far end of UART 0, optional
	peerLine bool // level the peer drives onto i_Rx_0

	quiet    int    // consecutive Clocks that left the Block quiescent
	executed uint64 // Clocks actually run, as distinct from skipped
}

// Peer is a device on the far end of UART 0's serial lines. Clock takes the
// level the Block is transmitting and returns the level to drive back onto
// i_Rx_0. Quiescent reports a peer that another Clock with the same input
// would leave unchanged.
type Peer interface {
	Clock(blockTx bool) bool
	Quiescent() bool
}

// SetPeer attaches a device to UART 0's lines. With a peer attached the
// i_Rx_0 line is the peer's output and SetRx(0, ...) is ignored.
func (b *Block) SetPeer(p Peer) {
	b.peer = p
	b.peerLine = true
}

// Power-up prescaler: 115200 baud at the 28 MHz system clock
// (uart.vhd:136, 324).
const defaultPrescalerLSB = 0x0F3

// NewBlock returns a Block in its power-up state.
func NewBlock() *Block {
	b := &Block{}
	for u := 0; u < 2; u++ {
		b.rx[u] = NewRX()
		b.tx[u] = NewTX()
		b.rxFifo[u] = NewFIFOP(9)
		b.txFifo[u] = NewFIFOP(6)
		b.framing[u] = 0x18
		b.prescLSB[u] = defaultPrescalerLSB
		b.rxDrv[u] = true
		b.oTx[u] = true
	}
	return b
}

// SetReset drives i_reset.
func (b *Block) SetReset(v bool) { b.reset = v }

// SetIO sets the register access pins: i_uart_reg, i_uart_rd, i_uart_wr and
// i_cpu_d. reg is 0 Rx/prescaler, 1 select, 2 frame, 3 Tx/status.
func (b *Block) SetIO(reg uint8, rd, wr bool, d byte) {
	b.reg, b.rd, b.wr, b.dIn = reg&3, rd, wr, d
}

// SetRx drives the i_Rx line of one UART from outside.
func (b *Block) SetRx(u int, level bool) { b.rxDrv[u] = level }

// SetLoopback connects a UART's o_Tx to its own i_Rx.
func (b *Block) SetLoopback(u int, on bool) { b.loop[u] = on }

// SetCTSn drives i_Tx_x_cts_n: false means the far end is ready to receive.
func (b *Block) SetCTSn(u int, ctsN bool) { b.ctsN[u] = ctsN }

func (b *Block) sel() int {
	if b.selectR {
		return 1
	}
	return 0
}

// Tx is o_Tx_x, the serial output (registered, uart.vhd:490).
func (b *Block) Tx(u int) bool { return b.oTx[u] }

// RxRTRn is o_Rx_x_rtr_n, low when the UART is ready to receive.
func (b *Block) RxRTRn(u int) bool { return b.oRtrN[u] }

// RxAvail is o_Rx_x_avail: at least one byte in the Rx FIFO.
func (b *Block) RxAvail(u int) bool { return b.oRxAvail[u] }

// RxNearFull is o_Rx_x_near_full: the Rx FIFO is at least 3/4 full.
func (b *Block) RxNearFull(u int) bool { return b.oRxNear[u] }

// RxErr is o_Rx_x_err: an overflow, framing or parity error is latched.
func (b *Block) RxErr(u int) bool { return b.oRxErr[u] }

// RxBreak is o_Rx_x_err_break: a break condition on the line.
func (b *Block) RxBreak(u int) bool { return b.oRxBreak[u] }

// TxEmpty is o_Tx_x_empty: the Tx FIFO is empty.
func (b *Block) TxEmpty(u int) bool { return b.oTxEmpty[u] }

// HWFlow is o_uartx_hwflow, framing register bit 5.
func (b *Block) HWFlow(u int) bool { return b.framing[u]&0x20 != 0 }

// rxStatusAvail etc are the uart.vhd status signals, from current state.
func (b *Block) rxStatusAvail(u int) bool { return !b.rxFifo[u].Empty() }
func (b *Block) txStatusEmpty(u int) bool {
	return b.txFifo[u].Empty() && !b.tx[u].Busy()
}
func (b *Block) rxStatusErr(u int) bool { return b.statusOverflow[u] || b.statusFraming[u] }

// CPUData is o_cpu_d: what a read of the currently selected register returns
// (uart.vhd:261-300). It reflects the state after the last Clock and the pins
// set since.
func (b *Block) CPUData() byte {
	u := b.sel()
	switch b.reg {
	case 0:
		if b.rxStatusAvail(u) {
			return byte(b.rxO[u])
		}
		return 0
	case 1:
		return byte(u)<<6 | b.prescMSB[u]
	case 2:
		return b.framing[u]
	}
	var v byte
	if b.rx[u].CombBreak() {
		v |= 0x80
	}
	if b.statusFraming[u] {
		v |= 0x40
	}
	if b.rxO[u]&0x100 != 0 && b.rxStatusAvail(u) {
		v |= 0x20
	}
	if b.txStatusEmpty(u) {
		v |= 0x10
	}
	if b.rxFifo[u].NearFull() {
		v |= 0x08
	}
	if b.statusOverflow[u] {
		v |= 0x04
	}
	if b.txFifo[u].Full() {
		v |= 0x02
	}
	if b.rxStatusAvail(u) {
		v |= 0x01
	}
	return v
}

func (b *Block) prescaler(u int) uint32 {
	return uint32(b.prescMSB[u])<<14 | uint32(b.prescLSB[u])
}

// Clock advances one i_CLK: the falling-edge Tx FIFO memory write, then the
// rising edge.
func (b *Block) Clock() {
	b.executed++
	// --- register decode (uart.vhd:176-199)
	var selectWr, frameWr, txWr, txRd, rxWr, rxRd bool
	switch b.reg {
	case 0:
		rxWr, rxRd = b.wr, b.rd
	case 1:
		selectWr = b.wr
	case 2:
		frameWr = b.wr
	default:
		txWr, txRd = b.wr, b.rd
	}
	selU := b.sel()

	// The PHYs see the pre-edge frame, prescaler and pins. Set them before
	// reading any combinational output (Busy, the error strobes) so those
	// reflect this cycle and not the last.
	for u := 0; u < 2; u++ {
		fr := b.framing[u]
		b.rx[u].SetFrame(fr&0x80 | fr&0x1F)
		b.rx[u].SetPrescaler(b.prescaler(u))
		b.rx[u].SetReset(b.reset)
		b.tx[u].SetFrame(fr)
		b.tx[u].SetPrescaler(b.prescaler(u))
		b.tx[u].SetCTS(!b.ctsN[u])
		b.tx[u].SetReset(b.reset)
	}

	var (
		txWrU, txRdU, rxRdU [2]bool
		txRdFE              [2]bool
		fifoReset           [2]bool
		rxFifoWE, txFifoWE  [2]bool
		txEn                [2]bool
		txByte              [2]byte
		rxAvailRaw          [2]bool
		rxByteRaw           [2]byte
		errFr, errPar       [2]bool
		errBrk              [2]bool
		rxStatErr           [2]bool
	)
	for u := 0; u < 2; u++ {
		on := selU == u
		txWrU[u] = txWr && on
		txRdU[u] = txRd && on
		rxRdU[u] = rxRd && on
		txRdFE[u] = b.txRdD[u] && !txRd
		fifoReset[u] = b.reset || b.framing[u]&0x80 != 0

		rxAvailRaw[u] = b.rx[u].Avail()
		rxByteRaw[u] = b.rx[u].Byte()
		errFr[u] = b.rx[u].CombFramingError()
		errPar[u] = b.rx[u].CombParityError()
		errBrk[u] = b.rx[u].CombBreak()
		rxStatErr[u] = b.rxStatusErr(u)

		// uart.vhd:603: cpu reads of this UART's Rx or status register have priority
		// over an Rx FIFO write (uart0_tx_rd is gated by the select, :168-169)
		rxFifoWE[u] = b.rxAvailD[u] && !b.rxFifo[u].Full() && !rxRdU[u] && !txRdU[u]
		txFifoWE[u] = txWrU[u] && !b.txWrD[u] && !b.txFifo[u].Full()
		txEn[u] = !b.tx[u].Busy() && !b.txFifo[u].Empty()
		txByte[u] = byte(b.txMem[u][b.txFifo[u].ReadAddr()])
	}

	// --- falling edge: Tx FIFO memory write (uart.vhd:482-490)
	for u := 0; u < 2; u++ {
		if txFifoWE[u] {
			b.txMem[u][b.txFifo[u].WriteAddr()] = uint16(b.dIn)
		}
	}

	// --- rising edge. Everything below reads pre-edge state.
	var (
		rtrN                        [2]bool
		nearFull, avail, txEmptyOut [2]bool
		newOverflow, newFraming     [2]bool
		newAvailD                   [2]bool
		newByteD                    [2]uint16
		newRxO                      [2]uint16
		txLevel                     [2]bool
	)
	for u := 0; u < 2; u++ {
		fr := b.framing[u]
		if fifoReset[u] {
			rtrN[u] = fr&0x20 != 0
		} else {
			rtrN[u] = fr&0x20 != 0 && b.rxFifo[u].AlmostFull()
		}
		nearFull[u] = b.rxFifo[u].NearFull()
		avail[u] = b.rxStatusAvail(u)
		txEmptyOut[u] = b.txFifo[u].Empty()

		if fifoReset[u] || txRdFE[u] {
			newOverflow[u], newFraming[u] = false, false
		} else {
			newOverflow[u] = b.statusOverflow[u] || (rxAvailRaw[u] && b.rxAvailD[u])
			newFraming[u] = b.statusFraming[u] || errFr[u] || errPar[u]
		}

		switch {
		case b.reset || rxFifoWE[u]:
			newAvailD[u], newByteD[u] = false, 0
		case rxAvailRaw[u] && !b.rxAvailD[u]:
			newAvailD[u] = true
			e := uint16(0)
			if rxStatErr[u] {
				e = 0x100
			}
			newByteD[u] = e | uint16(rxByteRaw[u])
		default:
			newAvailD[u], newByteD[u] = b.rxAvailD[u], b.rxByteD[u]
		}

		// Rx FIFO memory: read-first, address is the write pointer while a
		// write is happening and the read pointer otherwise (uart.vhd:595).
		addr := b.rxFifo[u].ReadAddr()
		if rxFifoWE[u] {
			addr = b.rxFifo[u].WriteAddr()
		}
		newRxO[u] = b.rxMem[u][addr]
		if rxFifoWE[u] {
			b.rxMem[u][addr] = b.rxByteD[u]
		}

		line := b.rxDrv[u]
		if u == 0 && b.peer != nil {
			line = b.peerLine
		}
		if b.loop[u] {
			line = b.oTx[u]
		}
		b.rx[u].Clock(line)

		if txEn[u] {
			b.tx[u].Send(txByte[u])
		}
		txLevel[u] = b.tx[u].Clock()

		// FIFO pointer managers
		if fifoReset[u] {
			b.rxFifo[u].Reset()
			b.txFifo[u].Reset()
		} else {
			b.rxFifo[u].SetRead(rxRdU[u])
			b.rxFifo[u].SetWrite(rxFifoWE[u])
			b.rxFifo[u].Clock()
			b.txFifo[u].SetRead(txEn[u])
			b.txFifo[u].SetWrite(txFifoWE[u])
			b.txFifo[u].Clock()
		}
	}

	// --- register updates (uart.vhd:222-357)
	if b.reset {
		b.selectR = false
	} else if selectWr {
		b.selectR = b.dIn&0x40 != 0
		if b.dIn&0x10 != 0 {
			b.prescMSB[b2i(b.selectR)] = b.dIn & 0x07
		}
	}
	if frameWr {
		b.framing[selU] = b.dIn
	}
	if rxWr {
		if b.dIn&0x80 == 0 {
			b.prescLSB[selU] = b.prescLSB[selU]&^0x7F | uint16(b.dIn&0x7F)
		} else {
			b.prescLSB[selU] = b.prescLSB[selU]&0x7F | uint16(b.dIn&0x7F)<<7
		}
	}
	for u := 0; u < 2; u++ {
		b.txRdD[u] = txRd && selU == u
		b.txWrD[u] = txWrU[u]
		b.statusOverflow[u], b.statusFraming[u] = newOverflow[u], newFraming[u]
		b.rxAvailD[u], b.rxByteD[u] = newAvailD[u], newByteD[u]
		b.rxO[u] = newRxO[u]
		b.oTx[u] = txLevel[u]
		b.oRtrN[u] = rtrN[u]
		b.oRxNear[u] = nearFull[u]
		b.oRxAvail[u] = avail[u]
		b.oRxErr[u] = rxStatErr[u]
		b.oRxBreak[u] = errBrk[u]
		b.oTxEmpty[u] = txEmptyOut[u]
	}
	if b.peer != nil {
		b.peerLine = b.peer.Clock(b.oTx[0])
	}
	if b.quiescentNow() {
		b.quiet++
	} else {
		b.quiet = 0
	}
}

// quiescentSettle is how many consecutive quiescent Clocks it takes for every
// registered output to have caught up with the idle state. The longest chain
// is the Rx FIFO memory read behind the pointer, two registers deep.
const quiescentSettle = 8

// quiescentNow reports a Block in which another Clock with the same pins
// changes nothing another module can see: no register access, no reset, both
// PHYs idle on a settled line, both FIFO managers settled, nothing in flight
// between the receiver and the Rx FIFO, and the peer idle.
func (b *Block) quiescentNow() bool {
	if b.reset || b.rd || b.wr {
		return false
	}
	for u := 0; u < 2; u++ {
		line := b.rxDrv[u]
		if u == 0 && b.peer != nil {
			line = b.peerLine
		}
		if b.loop[u] {
			line = b.oTx[u]
		}
		if !b.tx[u].idleStable() || !b.rx[u].idleStable(line) ||
			!b.txFifo[u].Empty() || !b.txFifo[u].settled() || !b.rxFifo[u].settled() ||
			b.rxAvailD[u] || b.txRdD[u] || b.txWrD[u] || !b.oTx[u] {
			return false
		}
	}
	return b.peer == nil || b.peer.Quiescent()
}

// Executed is the number of Clocks actually run. Advance skips a quiescent
// Block, so this is the work done, not the time elapsed.
func (b *Block) Executed() uint64 { return b.executed }

// Advance runs n Clocks. each, if non-nil, is called after every Clock that is
// actually executed with that Clock's index in [0, n). A Block that has been
// quiescent for quiescentSettle Clocks skips the rest of the span: nothing it
// drives changes and nothing it holds changes, so the skipped Clocks would
// have been identical.
func (b *Block) Advance(n int, each func(i int)) {
	for i := 0; i < n; i++ {
		if b.quiet >= quiescentSettle && b.quiescentNow() {
			return
		}
		b.Clock()
		if each != nil {
			each(i)
		}
	}
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
