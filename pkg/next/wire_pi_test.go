package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// NR$A0 in core 3.02.05: bit 1 (Pi SPI slave enable) reads back, bits 7, 6
// and 2 read zero, and bit 1 alone survives a reset.
func TestNRA0ReadShape(t *testing.T) {
	d := nextregs.New()
	WirePiPeripherals(d)
	d.WriteReg(0xA0, 0xFF)
	if got := d.ReadReg(0xA0); got != 0x3B {
		t.Errorf("NR$A0 wrote $FF read $%02X, want $3B", got)
	}
	d.WriteReg(0xA0, 0x02)
	if got := d.ReadReg(0xA0); got != 0x02 {
		t.Errorf("bit 1 alone read $%02X, want $02", got)
	}
	d.WriteReg(0xA0, 0xC4)
	if got := d.ReadReg(0xA0); got != 0x00 {
		t.Errorf("bits 7, 6 and 2 read $%02X, want $00", got)
	}
}

func TestNRA0Bit1SurvivesAResetAndTheRestDoesNot(t *testing.T) {
	d := nextregs.New()
	WirePiPeripherals(d)
	d.WriteReg(0xA0, 0xFF)
	d.Reset()
	if got := d.ReadReg(0xA0); got != 0x02 {
		t.Errorf("after reset NR$A0 = $%02X, want $02", got)
	}
	d.WriteReg(0xA0, 0x00) // a guest can still clear it
	d.Reset()
	if got := d.ReadReg(0xA0); got != 0x00 {
		t.Errorf("after clearing and resetting NR$A0 = $%02X, want $00", got)
	}
}
