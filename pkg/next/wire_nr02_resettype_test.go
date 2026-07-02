package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// nr02Harness wires a dispatcher + memory + CPU exactly like the
// existing WireReset tests (wire_test.go).
func nr02Harness(t *testing.T) *nextregs.Dispatcher {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireReset(disp, mem, cpu, nil)
	return disp
}

// TestNR02ResetTypeHistory — zxnext.vhd nr_02_reset_type is a 3-bit
// shift-history, NOT a simple sticky pair:
//
//	power-on:                       "100"  → NR$02 reads xx...00
//	after 1st soft reset (vhd:1736): bit1=old bit2, bit0=old1|old0
//	                                "010"  → reads ...10 ($02)
//	after 2nd soft reset:           "001"  → reads ...01 ($01)
//	(stays "001" for further softs: bit0 = 0|1 = 1)
//
// Read composition (vhd:5891): bus_reset(b7) & "00" & iotrap &
// mf_nmi & divmmc_nmi & reset_type(1:0). Bit 7 = nr_02_bus_reset,
// latched from bit 7 of EVERY NR$02 write (vhd:5119).
// NextZXOS distinguishes its first staging soft-reset from later
// ones via these low bits.
func TestNR02ResetTypeHistory(t *testing.T) {
	d := nr02Harness(t)

	// Direct-boot path (no FPGA bootrom): WireReset seeds "010" — the
	// bootrom's single soft reset already applied — so the OS's first
	// read returns $02 ("first pass, came from hard").
	if got := d.ReadReg(0x02) & 0x03; got != 0x02 {
		t.Fatalf("direct-boot cold NR$02 low bits = %#02x, want 02 (seed 010)", got)
	}

	d.WriteReg(0x02, 0x01) // the OS's staging soft reset → "001"
	if got := d.ReadReg(0x02) & 0x03; got != 0x01 {
		t.Errorf("after staging soft reset low bits = %#02x, want 01", got)
	}

	d.WriteReg(0x02, 0x01) // further softs stay "001"
	if got := d.ReadReg(0x02) & 0x03; got != 0x01 {
		t.Errorf("after further soft reset low bits = %#02x, want 01", got)
	}

	d.WriteReg(0x02, 0x02) // hard reset → fabric re-init "100" → read 00
	if got := d.ReadReg(0x02) & 0x03; got != 0x00 {
		t.Errorf("after hard reset low bits = %#02x, want 00 (reset_type=100)", got)
	}

	d.WriteReg(0x02, 0x01) // soft after hard → "010" → read 02
	if got := d.ReadReg(0x02) & 0x03; got != 0x02 {
		t.Errorf("soft-after-hard low bits = %#02x, want 02", got)
	}
}

// TestNR02ResetTypeSeed_FPGABootROM — the REAL FPGA-bootrom boot powers on
// at the VHDL "100" seed (zxnext.vhd:1306), NOT the direct-boot "010". When
// the FPGA bootrom is active at WireReset time, NR$02 must read $00 cold
// (reset_type "100") and $02 after the first soft reset ("100"->"010").
//
// This guards the call-order requirement that the FPGA bootrom must be
// loaded before next.Wire runs: NextZXOS's 128K-BASIC launch reads NR$02
// and branches on $02 (first staging pass) vs $01 (post-staging). With
// the wrong "010" seed, the lone $6D31 staging soft reset would shift to
// $01, so the OS would skip staging and the editor would derail. With
// the correct "100" seed it shifts to $02 and staging runs.
func TestNR02ResetTypeSeed_FPGABootROM(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	mem.SetFPGABootROM(make([]byte, 8192)) // bootrom active BEFORE WireReset
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireReset(disp, mem, cpu, nil)

	if got := disp.ReadReg(0x02) & 0x03; got != 0x00 {
		t.Fatalf("FPGA-bootrom cold NR$02 low bits = %#02x, want 00 (power-on seed 100)", got)
	}
	disp.WriteReg(0x02, 0x01) // bootrom/staging soft reset → "010"
	if got := disp.ReadReg(0x02) & 0x03; got != 0x02 {
		t.Errorf("after 1st soft reset = %#02x, want 02 (100->010 — the value NextZXOS reads to run 128K-BASIC staging)", got)
	}
}

// TestNR02BusResetBit7Latch — bit 7 follows the last written bit 7
// (assert with $80, drop with any bit7=0 write, e.g. the $01 soft
// reset that follows in NextZXOS's staging sequence).
func TestNR02BusResetBit7Latch(t *testing.T) {
	d := nr02Harness(t)

	d.WriteReg(0x02, 0x80)
	if d.ReadReg(0x02)&0x80 == 0 {
		t.Error("bit 7 not latched after $80 write")
	}
	d.WriteReg(0x02, 0x01)
	if d.ReadReg(0x02)&0x80 != 0 {
		t.Error("bit 7 not cleared by bit7=0 write")
	}
}
