package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/ctc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

type pin struct{ calls []bool }

func (p *pin) SetBusDelay(on bool) { p.calls = append(p.calls, on) }

func TestInstructionHookSamplesTheDMADelayPin(t *testing.T) {
	disp := nextregs.New()
	im2 := NewIM2Driver()
	clk := NewCTCClock(ctc.NewBank(), im2)
	u := NewUARTSystem(disp, im2, func() int { return 1 })
	p := &pin{}
	hook := InstructionHook(func() int { return 1 }, clk, u, im2, func() BusDelayer { return p })

	im2.SetMode(true)
	im2.SetEnabled(IM2SourceULA, true)
	im2.SetDMAEnabled(IM2SourceULA, true)
	hook(4)
	im2.Raise(IM2SourceULA)
	im2.Lower(IM2SourceULA)
	hook(4)
	im2.Acknowledge()
	hook(4)
	im2.RETISeen()
	hook(4)

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

func TestInstructionHookToleratesNoDMA(t *testing.T) {
	disp := nextregs.New()
	im2 := NewIM2Driver()
	u := NewUARTSystem(disp, im2, func() int { return 1 })
	InstructionHook(func() int { return 1 }, NewCTCClock(nil, im2), u, im2, nil)(4)
	InstructionHook(func() int { return 1 }, NewCTCClock(nil, im2), u, im2, func() BusDelayer { return nil })(4)
}
