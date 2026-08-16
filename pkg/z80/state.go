package z80

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete CPU state capture, for rewind and for replaying execution forward
// from a captured point.
//
// The processor was the one part of the machine with no capture at all. Every
// peripheral had one, so a rewind restored the RAM, the sound chip and the
// disk controller and left PC, the register file, the interrupt flip-flops and
// the T-state counter where the present had them. A machine resuming from the
// wrong instruction with the right memory is a stranger failure than one that
// restores nothing.
//
// What is captured is everything the processor carries between instructions.
// What is not is wiring and derived data, listed at the bottom with reasons.

// altRegs is the shadow register file, grouped so the wire form reads the way
// the hardware is described rather than as eight loose bytes.
type altRegs struct{ A, F, B, C, D, E, H, L byte }

// cpuState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running CPU that survives an
// instruction boundary is present.
type cpuState struct {
	A, F, B, C, D, E, H, L byte
	Alt                    altRegs
	IX, IY                 uint16
	SP, PC                 uint16
	I, R                   byte
	IFF1, IFF2             bool
	IM                     byte
	Halted                 bool
	HaltWakeOnInt          bool
	WZ                     uint16

	// BranchSource/BranchFrom are the debugger's branch annotation. They are
	// consumed and cleared by the M1 hook, so at an instruction boundary they
	// describe how the NEXT fetch was reached. Captured because a rewind that
	// dropped them would make the first recorded instruction after a restore
	// claim it was reached sequentially when it was not.
	BranchSource uint8
	BranchFrom   uint16

	// Interrupt state. IRQPending and PendingNMI are atomics on the CPU,
	// flattened to plain bools here: a capture is taken with the machine
	// stopped, so there is no concurrent writer to lose.
	IRQPending           bool
	PendingNMI           bool
	EIDelay              bool
	LineIntOffsetTstates uint64
	LineIntFired         bool
	FrameIntDisabled     bool
	IntAssertTstate      uint64
	IntPulseTstates      uint64
	FrameIntFired        bool
	FrameIntDeasserted   bool
	NextFrameBoundary    uint64
	IM2Vector            byte

	// Speed selection is guest-visible state on the Next (NextReg $07).
	SpeedSelect byte
	SpeedLocked bool

	// The counters. Restoring these is what makes replay-based reverse
	// execution able to say it landed on a given instruction: without the
	// instruction count, "re-execute forward to instruction N" has no N to
	// compare against.
	Tstates          uint64
	InstructionCount uint64
}

// StateID identifies the processor in a captured machine state.
func (c *CPU) StateID() string { return "cpu" }

// snapshot builds the wire form.
func (c *CPU) snapshot() cpuState {
	return cpuState{
		A: c.A, F: c.F, B: c.B, C: c.C, D: c.D, E: c.E, H: c.H, L: c.L,
		Alt: altRegs{A: c.A_, F: c.F_, B: c.B_, C: c.C_, D: c.D_, E: c.E_, H: c.H_, L: c.L_},
		IX:  c.IX, IY: c.IY,
		SP: c.SP, PC: c.PC,
		I: c.I, R: c.R,
		IFF1: c.IFF1, IFF2: c.IFF2,
		IM:     c.IM,
		Halted: c.Halted,

		HaltWakeOnInt: c.HaltWakeOnInt,
		WZ:            c.WZ,
		BranchSource:  c.BranchSource,
		BranchFrom:    c.BranchFrom,

		IRQPending:           c.IRQPending.Load(),
		PendingNMI:           c.PendingNMI.Load(),
		EIDelay:              c.eiDelay,
		LineIntOffsetTstates: c.LineIntOffsetTstates,
		LineIntFired:         c.lineIntFired,
		FrameIntDisabled:     c.FrameIntDisabled,
		IntAssertTstate:      c.IntAssertTstate,
		IntPulseTstates:      c.IntPulseTstates,
		FrameIntFired:        c.frameIntFired,
		FrameIntDeasserted:   c.frameIntDeasct,
		NextFrameBoundary:    c.nextFrameBoundary,
		IM2Vector:            c.IM2Vector,

		SpeedSelect: c.speedSelect,
		SpeedLocked: c.speedLocked,

		Tstates:          c.tstates,
		InstructionCount: c.instructionCount,
	}
}

// stateForTest exposes the wire form to this package's tests, so a test can
// assert that its drive sequence moves every captured field. Comparing blobs
// proves a field is restored; only comparing fields proves it was exercised.
func (c *CPU) stateForTest() cpuState { return c.snapshot() }

// SaveState captures the complete processor state.
func (c *CPU) SaveState() []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(c.snapshot()); err != nil {
		// Encoding fixed-size values cannot fail in practice, and this runs
		// from the capture hook, which skips a failed capture rather than
		// stopping the machine. A nil blob is rejected by LoadState, so a bad
		// capture surfaces as a failed restore instead of a crash mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded in full before any field is written, so a malformed
// capture leaves the processor untouched rather than half-restored. A
// half-restored CPU is the worst case in the machine: it runs, from a register
// file that never existed.
func (c *CPU) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("z80: empty state (the capture failed)")
	}
	var s cpuState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("z80: decoding state: %w", err)
	}

	c.A, c.F, c.B, c.C = s.A, s.F, s.B, s.C
	c.D, c.E, c.H, c.L = s.D, s.E, s.H, s.L
	c.A_, c.F_, c.B_, c.C_ = s.Alt.A, s.Alt.F, s.Alt.B, s.Alt.C
	c.D_, c.E_, c.H_, c.L_ = s.Alt.D, s.Alt.E, s.Alt.H, s.Alt.L
	c.IX, c.IY = s.IX, s.IY
	c.SP, c.PC = s.SP, s.PC
	c.I, c.R = s.I, s.R
	c.IFF1, c.IFF2 = s.IFF1, s.IFF2
	c.IM = s.IM
	c.Halted = s.Halted

	c.HaltWakeOnInt = s.HaltWakeOnInt
	c.WZ = s.WZ
	c.BranchSource = s.BranchSource
	c.BranchFrom = s.BranchFrom

	c.IRQPending.Store(s.IRQPending)
	c.PendingNMI.Store(s.PendingNMI)
	c.eiDelay = s.EIDelay
	c.LineIntOffsetTstates = s.LineIntOffsetTstates
	c.lineIntFired = s.LineIntFired
	c.FrameIntDisabled = s.FrameIntDisabled
	c.IntAssertTstate = s.IntAssertTstate
	c.IntPulseTstates = s.IntPulseTstates
	c.frameIntFired = s.FrameIntFired
	c.frameIntDeasct = s.FrameIntDeasserted
	c.nextFrameBoundary = s.NextFrameBoundary
	c.IM2Vector = s.IM2Vector

	c.speedSelect = s.SpeedSelect
	c.speedLocked = s.SpeedLocked

	c.tstates = s.Tstates
	c.instructionCount = s.InstructionCount
	return nil
}

// Deliberately NOT captured, and why:
//
//   - mem, ula, contender, NextRegs: the bus and the devices on it. Each is its
//     own capture, or is wiring rebuilt when the machine is constructed.
//     Capturing them here would restore one device from two blobs.
//   - Every hook (BreakpointCheck, TrapCheck, PreFetchHook, PostFetchHook,
//     M1FetchHook, preFetchHooks, postFetchHooks, OnSPLoad, NMICallback,
//     retnHook, StacklessReadNR/WriteNR): the debugger's and the host's
//     property, not the machine's. A rewind must not detach a breakpoint the
//     user set after the capture was taken.
//   - Variant, MemContend, NMIStackless, logEnabled: machine configuration,
//     fixed when the model is built. A capture taken on one model is refused
//     wholesale by the registry's device-set check rather than silently
//     reconfiguring the machine here.
//   - sz53Table, parityTable, halfcarry*/overflow* tables: constant lookup
//     tables computed at construction. Identical in every CPU, so capturing
//     them would add 1.5 KB per snapshot to restore values that cannot differ.
//   - currentInstrPC: set at the start of each instruction and dead at the
//     boundary where captures are taken.
