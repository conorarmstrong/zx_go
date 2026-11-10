package next

import "github.com/conorarmstrong/zx_go/pkg/next/ctc"

// CTCClock advances the Next's four-channel CTC from the CPU's
// instruction boundaries.
//
// The CTC is clocked by the FPGA's free-running i_CLK_28, not by the
// CPU clock (zxnext.vhd:4072). The CPU's T-state counter runs at the
// selected speed, so an instruction that consumes N CPU T-states spans
// N * 8 / SpeedMultiplier 28 MHz CTC clocks. The CPU's instruction hook
// supplies both values; there are no timers in the channel FSM, so
// advancing exactly that many clocks at the instruction boundary is
// equivalent to ticking it continuously (the same argument as the IM2
// driver, and the reason the ROADMAP requires a divider/deadline model
// rather than a per-instruction single tick).
//
// When no channel is running the tick loop is skipped entirely: a CTC
// configured but idle cannot change on a clock, and most software never
// programs it.
type CTCClock struct {
	bank *ctc.Bank
	im2  *IM2Driver
}

// NewCTCClock wires a CTC bank to the daisy chain. Either argument may be
// nil on a test path; Advance is a no-op then.
func NewCTCClock(bank *ctc.Bank, im2 *IM2Driver) *CTCClock {
	return &CTCClock{bank: bank, im2: im2}
}

// Advance consumes the CPU T-states of one instruction at the given CPU
// speed multiplier (1/2/4/8). It ticks the CTC's 28 MHz clock, raises the
// IM2 request for every channel whose registered ZC/TO pulse and interrupt
// enable are active, then drops the request again. The daisy chain latches
// the request edge, so the one-clock pulse survives until the CPU
// acknowledges it.
func (c *CTCClock) Advance(cpuTStates, speedMultiplier int) {
	if c == nil || c.bank == nil || cpuTStates <= 0 {
		return
	}
	if speedMultiplier < 1 {
		speedMultiplier = 1
	}
	ticks := cpuTStates * 8 / speedMultiplier
	if ticks < 1 {
		ticks = 1
	}
	if c.bank.Idle() {
		return
	}

	var pulse [ctc.NumChannels]bool
	for i := 0; i < ticks; i++ {
		zc := c.bank.TickChained()
		for ch := 0; ch < ctc.NumChannels; ch++ {
			if zc[ch] && c.bank.Channel(ch).IntEnabled() {
				pulse[ch] = true
			}
		}
	}
	if c.im2 == nil {
		return
	}
	for ch := 0; ch < ctc.NumChannels; ch++ {
		if !pulse[ch] {
			continue
		}
		src := IM2SourceCTC0 + ch
		c.im2.Raise(src)
		c.im2.Lower(src)
	}
}

// Bank returns the wired CTC bank (for tests and debugger introspection).
func (c *CTCClock) Bank() *ctc.Bank {
	if c == nil {
		return nil
	}
	return c.bank
}
