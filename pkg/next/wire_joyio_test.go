package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// TestNR0BJoyIOModeMask verifies NR$0B keeps only the writable bits
// (7=iomode_en, 5:4=mode, 0=pin0) per FPGA zxnext.vhd:5200-5203.
func TestNR0BJoyIOModeMask(t *testing.T) {
	d := nextregs.New()
	WireJoystickIOMode(d)
	d.WriteReg(0x0B, 0xFF)
	if got := d.ReadReg(0x0B); got != 0xB1 {
		t.Errorf("NR$0B write $FF → read $%02X, want $B1 (bits 7,5:4,0)", got)
	}
	d.WriteReg(0x0B, 0x70) // bit 6 reserved (dropped); bits 5:4 kept
	if got := d.ReadReg(0x0B); got != 0x30 {
		t.Errorf("NR$0B write $70 → read $%02X, want $30", got)
	}
}
