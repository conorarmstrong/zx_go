package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/ctc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

type pin struct{ calls []bool }

func (p *pin) SetBusDelay(on bool) { p.calls = append(p.calls, on) }

// fakeCPU is a CPU clock the test moves by hand, recording INT polls.
type fakeCPU struct {
	t     uint64
	polls []uint64
}

func (c *fakeCPU) Tstates() uint64           { return c.t }
func (c *fakeCPU) SpeedMultiplier() int      { return 1 }
func (c *fakeCPU) PollIntSources(now uint64) { c.polls = append(c.polls, now) }

func newTickerRig(t *testing.T, dma func() BusDelayer) (*Ticker, *fakeCPU, *IM2Driver, *ctc.Bank) {
	t.Helper()
	disp := nextregs.New()
	im2 := NewIM2Driver()
	bank := ctc.NewBank()
	u := NewUARTSystem(disp, im2, func() int { return 1 })
	cpu := &fakeCPU{t: 1000}
	return NewTicker(cpu, NewCTCClock(bank, im2), u, im2, dma), cpu, im2, bank
}

func TestTickerSamplesTheDMADelayPin(t *testing.T) {
	p := &pin{}
	tk, cpu, im2, _ := newTickerRig(t, func() BusDelayer { return p })
	im2.SetMode(true)
	im2.SetEnabled(IM2SourceULA, true)
	im2.SetDMAEnabled(IM2SourceULA, true)
	step := func() { cpu.t += 4; tk.Instruction(4) }
	step()
	im2.Raise(IM2SourceULA)
	im2.Lower(IM2SourceULA)
	step()
	im2.Acknowledge()
	step()
	im2.RETISeen()
	step()

	want := []bool{false, true, true, false}
	if len(p.calls) != len(want) {
		t.Fatalf("pin driven %v", p.calls)
	}
	for i := range want {
		if p.calls[i] != want[i] {
			t.Errorf("sample %d = %v, want %v (all %v)", i, p.calls[i], want[i], p.calls)
		}
	}
}

func TestTickerToleratesNoDMA(t *testing.T) {
	tk, cpu, _, _ := newTickerRig(t, nil)
	cpu.t += 4
	tk.Instruction(4)
	tk2, cpu2, _, _ := newTickerRig(t, func() BusDelayer { return nil })
	cpu2.t += 4
	tk2.Instruction(4)
	tk2.DMAByte(4)
}

// CTC time is the clock's, counted once: bytes of a DMA block advance it
// ahead of the clock, and the instruction that ends with the block's charge
// adds only the rest.
func TestTickerCountsBlockTimeOnce(t *testing.T) {
	// Two rigs: one sees a 100 T-state block byte by byte, the other only the
	// instruction that contains it. Their CTCs must agree.
	a, cpuA, _, bankA := newTickerRig(t, nil)
	b, cpuB, _, bankB := newTickerRig(t, nil)
	for _, bk := range []*ctc.Bank{bankA, bankB} {
		bk.WritePort(0x183B, 0x05) // timer mode, prescaler 16, time constant follows
		bk.WritePort(0x183B, 0x10)
	}
	start, _ := bankA.ReadPort(0x183B)
	for e := uint64(10); e < 100; e += 10 {
		a.DMAByte(e)
	}
	if mid, _ := bankA.ReadPort(0x183B); mid == start {
		t.Fatal("the CTC did not move during the block")
	}
	cpuA.t += 111 // 11 T-states of OUT plus the 100 charged
	a.Instruction(111)
	cpuB.t += 111
	b.Instruction(111)
	for ch := 0; ch < 4; ch++ {
		port := uint16(0x183B + ch*0x100)
		ga, _ := bankA.ReadPort(port)
		gb, _ := bankB.ReadPort(port)
		if ga != gb {
			t.Errorf("channel %d: per-byte %d, per-instruction %d", ch, ga, gb)
		}
	}
	if len(cpuA.polls) != 9 || cpuA.polls[0] != 1010 || cpuA.polls[8] != 1090 {
		t.Errorf("INT polls %v, want 1010..1090", cpuA.polls)
	}
}

// A clock that goes back (a state restore) runs nothing.
func TestTickerClockGoingBackRunsNothing(t *testing.T) {
	tk, cpu, _, bank := newTickerRig(t, nil)
	bank.WritePort(0x183B, 0x05)
	bank.WritePort(0x183B, 0x10)
	before, _ := bank.ReadPort(0x183B)
	cpu.t = 10
	tk.Instruction(4)
	if after, _ := bank.ReadPort(0x183B); after != before {
		t.Error("the CTC moved when the clock went back")
	}
}
