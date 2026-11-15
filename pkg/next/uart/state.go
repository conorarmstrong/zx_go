package uart

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture for the UART register block and the module behind
// UART 0, for rewind and save states.
//
// Everything the Block holds is in its fields: the register file, both
// PHYs, both FIFO pointer managers, the four FIFO memories, the delayed
// strobes and the registered outputs. The memories are captured whole, stale
// bytes included, because the status register's error bit and the Rx data
// register both read the memory at the read pointer, so a stale cell is
// observable.
//
// The pins (reg, rd, wr, dIn, the driven lines, the loopback and CTS
// switches) are captured too. They are inputs, but the machine sets them
// between Clocks and a capture can land between a Set and the next Clock.

type txPHYState struct {
	Frame         byte
	Prescaler     uint32
	CtsN          bool
	TxEn          bool
	TxByte        byte
	Reset         bool
	State         int
	TxShift       byte
	TxTimer       uint32
	TxBitCount    uint8
	TxParity      bool
	FrameParityEn bool
	FrameStopBits bool
	PrescLatched  uint32
}

type rxPHYState struct {
	Frame         byte
	Prescaler     uint32
	Reset         bool
	DbCounterSize uint
	DbButton      uint8
	DbCounter     uint32
	DbButtonDb    bool
	RxD           bool
	State         int
	RxShift       byte
	RxTimer       uint32
	RxTimerUpd    bool
	RxBitCount    uint8
	RxParity      bool
	FrameBits     uint8
	FrameParityEn bool
	FrameStopBits bool
	PrescLatched  uint32
	RxAvail       bool
	ErrFr         bool
	ErrPar        bool
}

type fifopState struct {
	Stored, RdAddr, WrAddr uint32
	RdDly, WrDly, Rd, Wr   bool
}

type blockState struct {
	Reset, Rd, Wr     bool
	Reg               uint8
	DIn               byte
	RxDrv, Loop, CtsN [2]bool

	SelectR  bool
	PrescMSB [2]uint8
	PrescLSB [2]uint16
	Framing  [2]byte

	TX     [2]txPHYState
	RX     [2]rxPHYState
	RxFifo [2]fifopState
	TxFifo [2]fifopState
	RxMem  [2][]uint16
	TxMem  [2][]uint16
	RxO    [2]uint16

	TxRdD, TxWrD   [2]bool
	StatusOverflow [2]bool
	StatusFraming  [2]bool
	RxAvailD       [2]bool
	RxByteD        [2]uint16

	OTx, ORtrN, ORxNear, ORxAvail, ORxErr, ORxBreak, OTxEmpty [2]bool

	HasPeer  bool
	PeerLine bool
	PeerCut  bool
	Peer     []byte
	Quiet    int
	Executed uint64
}

type espState struct {
	RX   rxPHYState
	TX   txPHYState
	App  []byte
	Line bool
	Out  bool
}

func (t *TX) save() txPHYState {
	return txPHYState{t.frame, t.prescaler, t.ctsN, t.txEn, t.txByte, t.reset,
		int(t.state), t.txShift, t.txTimer, t.txBitCount, t.txParity,
		t.frameParityEn, t.frameStopBits, t.prescLatched}
}

func (t *TX) load(s txPHYState) {
	t.frame, t.prescaler, t.ctsN, t.txEn, t.txByte, t.reset = s.Frame, s.Prescaler, s.CtsN, s.TxEn, s.TxByte, s.Reset
	t.state, t.txShift, t.txTimer, t.txBitCount, t.txParity = txState(s.State), s.TxShift, s.TxTimer, s.TxBitCount, s.TxParity
	t.frameParityEn, t.frameStopBits, t.prescLatched = s.FrameParityEn, s.FrameStopBits, s.PrescLatched
}

func (r *RX) save() rxPHYState {
	return rxPHYState{r.frame, r.prescaler, r.reset,
		r.db.counterSize, r.db.button, r.db.counter, r.db.buttonDb, r.rxD,
		int(r.state), r.rxShift, r.rxTimer, r.rxTimerUpd, r.rxBitCount, r.rxParity,
		r.frameBits, r.frameParityEn, r.frameStopBits, r.prescLatched,
		r.rxAvail, r.errFr, r.errPar}
}

func (r *RX) load(s rxPHYState) {
	r.frame, r.prescaler, r.reset = s.Frame, s.Prescaler, s.Reset
	r.db = &debounce{counterSize: s.DbCounterSize, button: s.DbButton, counter: s.DbCounter, buttonDb: s.DbButtonDb}
	r.rxD = s.RxD
	r.state, r.rxShift, r.rxTimer, r.rxTimerUpd = rxState(s.State), s.RxShift, s.RxTimer, s.RxTimerUpd
	r.rxBitCount, r.rxParity = s.RxBitCount, s.RxParity
	r.frameBits, r.frameParityEn, r.frameStopBits, r.prescLatched = s.FrameBits, s.FrameParityEn, s.FrameStopBits, s.PrescLatched
	r.rxAvail, r.errFr, r.errPar = s.RxAvail, s.ErrFr, s.ErrPar
}

func (f *FIFOP) save() fifopState {
	return fifopState{f.stored, f.rdAddr, f.wrAddr, f.rdDly, f.wrDly, f.rd, f.wr}
}

func (f *FIFOP) load(s fifopState) {
	f.stored, f.rdAddr, f.wrAddr, f.rdDly, f.wrDly, f.rd, f.wr = s.Stored, s.RdAddr, s.WrAddr, s.RdDly, s.WrDly, s.Rd, s.Wr
}

// StateID identifies the register block in a captured machine state.
func (b *Block) StateID() string { return "next.uart" }

// peerState is what a Peer provides to be captured with the Block.
type peerState interface {
	SaveState() []byte
	LoadState([]byte) error
}

// SaveState captures the complete Block, including an attached peer.
func (b *Block) SaveState() []byte {
	s := blockState{
		Reset: b.reset, Rd: b.rd, Wr: b.wr, Reg: b.reg, DIn: b.dIn,
		RxDrv: b.rxDrv, Loop: b.loop, CtsN: b.ctsN,
		SelectR: b.selectR, PrescMSB: b.prescMSB, PrescLSB: b.prescLSB, Framing: b.framing,
		RxO: b.rxO, TxRdD: b.txRdD, TxWrD: b.txWrD,
		StatusOverflow: b.statusOverflow, StatusFraming: b.statusFraming,
		RxAvailD: b.rxAvailD, RxByteD: b.rxByteD,
		OTx: b.oTx, ORtrN: b.oRtrN, ORxNear: b.oRxNear, ORxAvail: b.oRxAvail,
		ORxErr: b.oRxErr, ORxBreak: b.oRxBreak, OTxEmpty: b.oTxEmpty,
		PeerLine: b.peerLine, PeerCut: b.peerCut, Quiet: b.quiet, Executed: b.executed,
	}
	for u := 0; u < 2; u++ {
		s.TX[u], s.RX[u] = b.tx[u].save(), b.rx[u].save()
		s.RxFifo[u], s.TxFifo[u] = b.rxFifo[u].save(), b.txFifo[u].save()
		s.RxMem[u] = append([]uint16(nil), b.rxMem[u][:]...)
		s.TxMem[u] = append([]uint16(nil), b.txMem[u][:]...)
	}
	if b.peer != nil {
		s.HasPeer = true
		if p, ok := b.peer.(peerState); ok {
			s.Peer = p.SaveState()
		}
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a capture. A capture taken with a peer needs one attached
// to restore into, and the reverse: the device set is part of what the state
// says.
func (b *Block) LoadState(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("uart: empty state (the capture failed)")
	}
	var s blockState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("uart: decoding state: %w", err)
	}
	if s.HasPeer != (b.peer != nil) {
		return fmt.Errorf("uart: state was captured with peer=%v, this machine has peer=%v", s.HasPeer, b.peer != nil)
	}
	if b.peer != nil {
		p, ok := b.peer.(peerState)
		if !ok {
			return fmt.Errorf("uart: the attached peer cannot restore state")
		}
		if err := p.LoadState(s.Peer); err != nil {
			return err
		}
	}
	b.reset, b.rd, b.wr, b.reg, b.dIn = s.Reset, s.Rd, s.Wr, s.Reg, s.DIn
	b.rxDrv, b.loop, b.ctsN = s.RxDrv, s.Loop, s.CtsN
	b.selectR, b.prescMSB, b.prescLSB, b.framing = s.SelectR, s.PrescMSB, s.PrescLSB, s.Framing
	b.rxO, b.txRdD, b.txWrD = s.RxO, s.TxRdD, s.TxWrD
	b.statusOverflow, b.statusFraming = s.StatusOverflow, s.StatusFraming
	b.rxAvailD, b.rxByteD = s.RxAvailD, s.RxByteD
	b.oTx, b.oRtrN, b.oRxNear, b.oRxAvail = s.OTx, s.ORtrN, s.ORxNear, s.ORxAvail
	b.oRxErr, b.oRxBreak, b.oTxEmpty = s.ORxErr, s.ORxBreak, s.OTxEmpty
	b.peerLine, b.peerCut, b.quiet, b.executed = s.PeerLine, s.PeerCut, s.Quiet, s.Executed
	for u := 0; u < 2; u++ {
		b.tx[u].load(s.TX[u])
		b.rx[u].load(s.RX[u])
		b.rxFifo[u].load(s.RxFifo[u])
		b.txFifo[u].load(s.TxFifo[u])
		if len(s.RxMem[u]) != len(b.rxMem[u]) || len(s.TxMem[u]) != len(b.txMem[u]) {
			return fmt.Errorf("uart: FIFO memory size mismatch")
		}
		copy(b.rxMem[u][:], s.RxMem[u])
		copy(b.txMem[u][:], s.TxMem[u])
	}
	return nil
}

// SaveState and LoadState capture the module on UART 0.
func (e *ESP) SaveState() []byte {
	s := espState{RX: e.rx.save(), TX: e.tx.save(), App: e.app.save(), Line: e.line, Out: e.out}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		return nil
	}
	return buf.Bytes()
}

func (e *ESP) LoadState(data []byte) error {
	var s espState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("uart: decoding ESP state: %w", err)
	}
	e.rx.load(s.RX)
	e.tx.load(s.TX)
	if err := e.app.load(s.App); err != nil {
		return err
	}
	e.line, e.out = s.Line, s.Out
	return nil
}

type appState struct {
	RxBuf   []byte
	TxBuf   []byte
	Version string
}

func (u *UART) save() []byte {
	var buf bytes.Buffer
	_ = gob.NewEncoder(&buf).Encode(appState{u.rxBuf, u.txBuf, u.version})
	return buf.Bytes()
}

func (u *UART) load(data []byte) error {
	var s appState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("uart: decoding AT responder state: %w", err)
	}
	u.rxBuf = append([]byte(nil), s.RxBuf...)
	u.txBuf = append([]byte(nil), s.TxBuf...)
	u.version = s.Version
	return nil
}
