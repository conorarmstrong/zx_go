package next

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/ctc"
	"github.com/conorarmstrong/zx_go/pkg/next/dma"
	"github.com/conorarmstrong/zx_go/pkg/next/uart"
)

func TestIM2DebugLinesShowSourceState(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)
	d.SetVectorBase(0xA0)
	d.SetEnabled(IM2SourceULA, true)
	d.SetDMAEnabled(IM2SourceULA, true)
	d.Raise(IM2SourceULA)
	d.Lower(IM2SourceULA)
	lines := strings.Join(d.DebugLines(), "\n")
	for _, want := range []string{"hardware IM2", "vector base $A0", "dma_int true", "ula"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in:\n%s", want, lines)
		}
	}
	row := d.DebugLines()[3+IM2SourceULA]
	if !strings.Contains(row, "requesting") || !strings.Contains(row, "y   y") {
		t.Errorf("ULA row %q: want requesting with enable and dma enable", row)
	}
}

func TestNMIDebugLines(t *testing.T) {
	d, _, _, rc := nmiRig(t)
	d.WriteReg(0x02, 0x08)
	lines := strings.Join(rc.NMIDebugLines(), "\n")
	for _, want := range []string{"fetch", "owner multiface", "mf true", "m1-en true"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in:\n%s", want, lines)
		}
	}
}

// The other blocks' views are smoke-tested for shape; they read only.
func TestBlockDebugLines(t *testing.T) {
	if n := len(dma.New(nil).DebugLines()); n != 5 {
		t.Errorf("dma: %d lines", n)
	}
	if n := len(uart.NewBlock().DebugLines()); n != 8 {
		t.Errorf("uart: %d lines", n)
	}
	bank := ctc.NewBank()
	bank.WritePort(0x183B, 0xA5)
	bank.WritePort(0x183B, 0x00)
	l := bank.DebugLines()
	if len(l) != 4 || !strings.Contains(l[0], "presc 256") || !strings.Contains(l[0], "int true") {
		t.Errorf("ctc: %q", l)
	}
}
