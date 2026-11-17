package next

// BusDelayer is the zxnDMA's dma_delay_i pin.
type BusDelayer interface{ SetBusDelay(on bool) }

// TickerCPU is what the Ticker needs from the CPU: its T-state clock, its speed
// multiplier, and a way to place the frame and line INTs at a T-state the
// clock has not reached yet.
type TickerCPU interface {
	Tstates() uint64
	SpeedMultiplier() int
	PollIntSources(now uint64)
	// Clock is the running T-state count, unbroken by the frame rewinds;
	// ClockGeneration changes where it jumps (reset, state load).
	Clock() uint64
	ClockGeneration() uint64
}

// Ticker keeps the parts of the Next that run beside the CPU (the CTC and the
// UARTs) in step with the CPU clock, and drives the zxnDMA's dma_delay_i pin
// from the IM2 chain.
//
// It runs at every instruction boundary (Instruction, the CPU's instruction
// hook) and after every byte of a continuous DMA block (DMAByte, the DMA's
// byte hook). The second matters because a continuous block runs to its end
// inside one port write with the CPU frozen, while on the FPGA the CTC, the
// UARTs and the ULA keep running and any of them can leave idle mid-block and
// make the DMA yield at the next byte (dma.vhd:427-428).
//
// It follows the clock rather than the instruction hook's deltas so that time
// charged to the clock outside an instruction (a parked block resuming at an
// instruction boundary) is not lost.
type Ticker struct {
	cpu  TickerCPU
	ctc  *CTCClock
	uart *UARTSystem
	im2  *IM2Driver
	dma  func() BusDelayer
	last uint64 // the Clock the CTC and the UARTs have been advanced to
	gen  uint64 // the ClockGeneration last seen
	nmi  HotkeyApplier
}

// HotkeyApplier applies NMI button presses queued from the UI goroutine.
type HotkeyApplier interface{ ApplyHotkeys() }

// SetHotkeys installs the NMI buttons' apply step, run at every instruction
// boundary, halted ones included.
func (t *Ticker) SetHotkeys(h HotkeyApplier) { t.nmi = h }

// NewTicker builds a Ticker. dma is a function because the machine builds the
// DMA after the hook is installed; it may be nil or return nil.
func NewTicker(cpu TickerCPU, ctc *CTCClock, uart *UARTSystem, im2 *IM2Driver, dma func() BusDelayer) *Ticker {
	return &Ticker{cpu: cpu, ctc: ctc, uart: uart, im2: im2, dma: dma, last: cpu.Clock(), gen: cpu.ClockGeneration()}
}

// Instruction is the CPU's instruction hook. The delta it is given is ignored:
// the clock is the reference.
func (t *Ticker) Instruction(int) {
	if t.nmi != nil {
		t.nmi.ApplyHotkeys()
	}
	t.syncTo(t.cpu.Clock())
}

// DMAByte is the DMA's byte hook. elapsed is the bus time the block has taken
// in this run, which the CPU clock will be charged only when the run ends, so
// the machine's time is the clock plus elapsed. The ULA's INTs are placed
// first, then the CTC and the UARTs catch up, then the pin is sampled for the
// next byte (see IM2Driver.DMADelay).
func (t *Ticker) DMAByte(elapsed uint64) {
	t.cpu.PollIntSources(t.cpu.Tstates() + elapsed)
	t.syncTo(t.cpu.Clock() + elapsed)
}

// syncTo runs the CTC and the UARTs up to now, a Clock value. Across a reset
// or a state load the clock jumps, and there is nothing to run.
func (t *Ticker) syncTo(now uint64) {
	if g := t.cpu.ClockGeneration(); g != t.gen {
		t.gen = g
		t.last = now
	}
	switch {
	case now > t.last:
		n, s := int(now-t.last), t.cpu.SpeedMultiplier()
		t.ctc.Advance(n, s)
		t.uart.Advance(n, s)
		t.last = now
	case now < t.last:
		// Only a clock set back by hand gets here; follow it.
		t.last = now
	}
	if t.dma == nil {
		return
	}
	if b := t.dma(); b != nil {
		b.SetBusDelay(t.im2.DMADelay())
	}
}
