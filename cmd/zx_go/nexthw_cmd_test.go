package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/next"
	"github.com/conorarmstrong/zx_go/pkg/next/ctc"
	"github.com/conorarmstrong/zx_go/pkg/next/dma"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

func TestCmdHardwareViews(t *testing.T) {
	d := &remoteDebugger{emu: &emulator{}}
	for _, name := range []string{"im2", "nmi", "dma", "uart", "ctc"} {
		if got := d.cmdHardware(name); !strings.HasPrefix(got, "ERR") {
			t.Errorf("%s on a classic machine: %q, want ERR", name, got)
		}
	}
	d.emu.nextIM2 = next.NewIM2Driver()
	d.emu.nextDMA = dma.New(nil)
	d.emu.nextCTC = ctc.NewBank()
	for name, want := range map[string]string{"im2": "uart1 tx", "dma": "dma_delay_i", "ctc": "ch3"} {
		if got := d.cmdHardware(name); !strings.HasPrefix(got, "OK") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want OK with %q", name, got, want)
		}
	}
}

func TestNextDebugProviderHardwareViews(t *testing.T) {
	e := &emulator{nextRegs: nextregs.New(), nextIM2: next.NewIM2Driver(), nextDMA: dma.New(nil), nextCTC: ctc.NewBank()}
	p := &nextDebugProvider{emu: e}
	for view, want := range map[string]string{
		debugger.HardwareViewNextRegs:   "$C0 Interrupt Control",
		debugger.HardwareViewInterrupts: "IM2 chain",
		debugger.HardwareViewDMA:        "dma_delay_i",
		debugger.HardwareViewSerial:     "CTC",
	} {
		got := strings.Join(p.HardwareView(view), "\n")
		if !strings.Contains(got, want) {
			t.Errorf("%s view missing %q:\n%s", view, want, got)
		}
	}
	if p.HardwareView(debugger.HardwareViewCopper) != nil {
		t.Error("copper view without a copper should be nil")
	}
	traced := false
	e.nextRegs.SetTracer(func(byte, byte, bool) { traced = true })
	p.NextRegs()
	p.HardwareView(debugger.HardwareViewNextRegs)
	if traced {
		t.Error("the GUI's register reads reached the tracer")
	}
}
