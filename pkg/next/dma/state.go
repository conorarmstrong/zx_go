package dma

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The latched configuration — the addresses, the length, the port modes — is
// the part a program wrote and the small part. What decides what the chip does
// next is the position it has reached, and the zxnDMA has three of those, none
// of which the guest can write and two of which it cannot read either:
//
//   - The transfer engine's position. curA/curB are the live pointers, not the
//     programmed start addresses: LOAD copies the starts in, every byte moves
//     them on, and CONTINUE deliberately resumes from where they are rather
//     than from where the block began. counter is the byte counter the read
//     mask returns, which in Z80-DMA mode starts at -1 rather than 0.
//   - A burst transfer with a fixed-time prescaler does not finish at ENABLE.
//     It moves one byte every prescaler T-states from Step() while the CPU runs
//     in the gaps, so at any instruction boundary — which is exactly where a
//     capture is taken — the block is part done: activeBurst, remaining and the
//     absolute T-state nextDue are the whole of what is left of it. Restore
//     without them and the streamed audio the transfer was feeding stops dead,
//     or restarts from the top.
//   - The read-back sequence cursor. readReg is the FPGA's reg_rd_seq_s: which
//     of the seven internal registers the NEXT port read returns. A driver
//     polling the byte counter reads two registers per poll; restore the mask
//     without the cursor and every poll afterwards is off by one, so the driver
//     reads the counter's high byte as its low byte.
//
// And the follow-byte queue is state for the same reason the palette's NR$44
// pairing phase is. The command stream is variable-length — a base byte's bits
// announce which bytes come next — and one OUT delivers one byte, so a capture
// lands between a base byte and the bytes it announced as often as not. A
// controller restored without the queue reads the next address byte as a base
// byte: $08 stops being "length low" and becomes a WR2 write, and the transfer
// that follows moves one byte from the wrong place instead of the block that
// was programmed. That is why pending holds codes rather than the closures it
// used to (see pendingOp): a closure cannot be captured, and a device whose
// state cannot be captured is a device that cannot be rewound.
//
// What is NOT here, and why:
//
//   - mem and io, the memory and IO buses. They are the wiring to the rest of
//     the machine, not this device's state; both capture themselves under their
//     own identifiers, and a rewind must not re-point the controller at
//     different objects.
//   - cycleSink and clock, the callbacks into the CPU clock, installed by the
//     emulator at construction. Same argument. Note that nextDue is captured as
//     an absolute reading of that clock, which is meaningful only because the
//     CPU restores its T-state counter from the same capture; a rewind that
//     moved one without the other would leave the burst either stalled or
//     firing its whole remainder at once.
//   - dmaTrace, a package-level debug flag read from the environment.
//
// Every other field of the running controller is captured. inTransfer is
// included for completeness even though no capture can observe it set — a
// transfer completes inside the WriteCommand or Step call that started it, and
// captures are taken between CPU instructions — because a wire form that is a
// total description of the struct is one that a new field cannot quietly fall
// out of.

// dmaState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running controller is present: adding
// a field to DMA without adding it here is the failure this package's tests are
// built to catch.
type dmaState struct {
	// Latched configuration (WR0/WR1/WR2/WR4/WR5).
	PortAStart uint16
	PortBStart uint16
	BlockLen   uint16
	AToB       bool
	AMode      byte
	BMode      byte
	AIsIO      bool
	BIsIO      bool

	// The DMA-mode latch. The ULA re-latches it from the access port on every
	// DMA port access, but it decides what LOAD/CONTINUE seed the byte counter
	// with, so a capture between the latch and the LOAD has to carry it.
	ZMode bool

	// Transfer engine position.
	CurA    uint16
	CurB    uint16
	Counter uint16

	// Timing and transfer mode.
	ACycleLen    byte
	BCycleLen    byte
	Prescaler    byte
	Mode         byte
	AutoRestart  bool
	EndOfBlock   bool
	AtLeastOne   bool
	LastDuration uint64

	// Read-back register file: the mask and the sequence cursor.
	ReadMask byte
	ReadReg  int

	// Interleaved-burst engine.
	ActiveBurst bool
	Remaining   int
	NextDue     uint64

	InTransfer bool

	// BusDelay is the dma_delay_i pin as this controller last saw it, and
	// Stalled is the transfer FSM parked in START_DMA because of it. The pin is
	// an input rather than a register, and it is captured for the same reason
	// ZMode is: the source is outside this device, but what the DMA does next
	// depends on the value it is holding, and a capture taken while a block is
	// parked has to come back parked. Restore Stalled without BusDelay and the
	// controller resumes a block the recorded machine had stopped.
	BusDelay bool
	Stalled  bool

	// Wedged is the register-write sequencer parked in the FPGA's unimplemented
	// R4_BYTE_2 state, where it swallows every command byte until the reset pin
	// is driven. A rewind that restores it clear hands the guest a controller
	// hardware would have left dead.
	Wedged bool

	// Pending is the follow-byte queue as pendingOp codes, widened to bytes so
	// gob encodes it as one byte string.
	Pending []byte
}

// StateID identifies the zxnDMA controller in a captured machine state.
func (d *DMA) StateID() string { return "next.dma" }

// SaveState captures the complete controller state.
func (d *DMA) SaveState() []byte {
	s := dmaState{
		PortAStart: d.portAStart,
		PortBStart: d.portBStart,
		BlockLen:   d.blockLen,
		AToB:       d.aToB,
		AMode:      d.aMode,
		BMode:      d.bMode,
		AIsIO:      d.aIsIO,
		BIsIO:      d.bIsIO,

		ZMode: d.zMode,

		CurA:    d.curA,
		CurB:    d.curB,
		Counter: d.counter,

		ACycleLen:    d.aCycleLen,
		BCycleLen:    d.bCycleLen,
		Prescaler:    d.prescaler,
		Mode:         d.mode,
		AutoRestart:  d.autoRestart,
		EndOfBlock:   d.endOfBlock,
		AtLeastOne:   d.atLeastOne,
		LastDuration: d.lastDuration,

		ReadMask: d.readMask,
		ReadReg:  d.readReg,

		ActiveBurst: d.activeBurst,
		Remaining:   d.remaining,
		NextDue:     d.nextDue,

		InTransfer: d.inTransfer,

		BusDelay: d.busDelay,
		Stalled:  d.stalled,
		Wedged:   d.wedged,
	}
	// Copied, not aliased: the live queue is resliced as each follow byte
	// arrives and appended to from inside one, so its backing array is reused
	// under a capture taken ahead of those bytes.
	if len(d.pending) > 0 {
		s.Pending = make([]byte, len(d.pending))
		for i, op := range d.pending {
			s.Pending[i] = byte(op)
		}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size values and one short byte slice cannot fail for
		// any reason the caller could act on, and returning an error here would
		// push a dead branch into every caller. A nil blob is rejected by
		// LoadState, so even an impossible failure surfaces as a failed restore
		// rather than a silently reset controller.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded and validated in full before any of it is applied, so a
// malformed state leaves the controller untouched rather than half-rewound. The
// three checks are the values that would not merely be wrong but would wedge
// the device: a read cursor outside the seven-register file, a follow-byte code
// this build has no case for, and a negative outstanding count, which leaves
// the burst engine active with no byte able to reach zero and finish it.
func (d *DMA) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("dma: empty state (the capture failed)")
	}
	var s dmaState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("dma: decoding state: %w", err)
	}
	if s.ReadReg < 0 || s.ReadReg > 6 {
		return fmt.Errorf("dma: state has read-sequence register %d, outside the seven read-back registers", s.ReadReg)
	}
	if s.Remaining < 0 {
		return fmt.Errorf("dma: state has %d bytes outstanding in the burst engine", s.Remaining)
	}
	pending := make([]pendingOp, len(s.Pending))
	for i, op := range s.Pending {
		if pendingOp(op) > opLast {
			return fmt.Errorf("dma: state has unknown follow-byte code %d at queue position %d", op, i)
		}
		pending[i] = pendingOp(op)
	}

	d.portAStart = s.PortAStart
	d.portBStart = s.PortBStart
	d.blockLen = s.BlockLen
	d.aToB = s.AToB
	d.aMode = s.AMode
	d.bMode = s.BMode
	d.aIsIO = s.AIsIO
	d.bIsIO = s.BIsIO

	d.zMode = s.ZMode

	d.curA = s.CurA
	d.curB = s.CurB
	d.counter = s.Counter

	d.aCycleLen = s.ACycleLen
	d.bCycleLen = s.BCycleLen
	d.prescaler = s.Prescaler
	d.mode = s.Mode
	d.autoRestart = s.AutoRestart
	d.endOfBlock = s.EndOfBlock
	d.atLeastOne = s.AtLeastOne
	d.lastDuration = s.LastDuration

	d.readMask = s.ReadMask
	d.readReg = s.ReadReg

	d.activeBurst = s.ActiveBurst
	d.remaining = s.Remaining
	d.nextDue = s.NextDue

	d.inTransfer = s.InTransfer
	d.busDelay = s.BusDelay
	d.stalled = s.Stalled
	d.wedged = s.Wedged
	d.pending = pending
	return nil
}
