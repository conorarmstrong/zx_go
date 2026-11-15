package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// o_dma_int is high from the moment a device with its dma-interrupt enable set
// leaves idle until it returns there (im2_device.vhd:151): requesting,
// acknowledged and in service all hold it, RETI releases it.
func TestIM2DMAIntFollowsTheDeviceFromRequestToRETI(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)
	d.SetEnabled(IM2SourceULA, true)
	d.SetDMAEnabled(IM2SourceULA, true)

	if d.DMAInt() || d.DMADelay() {
		t.Fatal("pin high with nothing requesting")
	}
	d.Raise(IM2SourceULA)
	d.Lower(IM2SourceULA)
	if !d.DMAInt() {
		t.Error("not high while requesting")
	}
	if _, ok := d.Acknowledge(); !ok {
		t.Fatal("no acknowledge")
	}
	if !d.DMAInt() {
		t.Error("not high while in service")
	}
	d.RETISeen()
	if d.DMAInt() || d.DMADelay() {
		t.Error("still high after RETI")
	}
}

// The enable gates the output per device, so a device that is in service but
// whose bit is clear leaves the DMA alone.
func TestIM2DMAIntNeedsTheDevicesOwnEnable(t *testing.T) {
	d := NewIM2Driver()
	d.SetMode(true)
	d.SetEnabled(IM2SourceCTC0+1, true)
	d.SetDMAEnabled(IM2SourceCTC0, true) // a different device's bit
	d.Raise(IM2SourceCTC0 + 1)
	d.Lower(IM2SourceCTC0 + 1)
	if d.DMAInt() {
		t.Error("another device's enable raised the pin")
	}
	d.SetDMAEnabled(IM2SourceCTC0+1, true)
	if !d.DMAInt() {
		t.Error("its own enable did not raise the pin")
	}
}

// NR$CC, CD and CE map onto chain indices as zxnext.vhd:1957-1958 lays the
// vector out. Each register bit is set alone and the one source it should
// reach is confirmed to be the only one that raises the pin.
func TestNRCCCDCEMapToTheRightSources(t *testing.T) {
	type bitCase struct {
		reg    byte
		bit    uint
		source []int
	}
	cases := []bitCase{
		{0xCC, 1, []int{IM2SourceLine}},
		{0xCC, 0, []int{IM2SourceULA}},
		{0xCE, 0, []int{IM2SourceUART0R}},
		{0xCE, 1, []int{IM2SourceUART0R}},
		{0xCE, 2, []int{IM2SourceUART0T}},
		{0xCE, 4, []int{IM2SourceUART1R}},
		{0xCE, 5, []int{IM2SourceUART1R}},
		{0xCE, 6, []int{IM2SourceUART1T}},
	}
	for b := uint(0); b < 8; b++ {
		cases = append(cases, bitCase{0xCD, b, []int{IM2SourceCTC0 + int(b)}})
	}
	for _, c := range cases {
		disp := nextregs.New()
		d := NewIM2Driver()
		WirePeripheralMasks(disp)
		WireDMAInterruptEnables(disp, d)
		disp.WriteReg(c.reg, 1<<c.bit)
		d.DMAInt() // refreshes the enables from the registers
		for src := 0; src < IM2NumPeriph; src++ {
			// Put exactly one device out of idle at a time.
			one := NewIM2Driver()
			one.dmaEnabled = d.dmaEnabled
			one.SetMode(true)
			one.SetEnabled(src, true)
			one.Raise(src)
			one.Lower(src)
			want := false
			for _, s := range c.source {
				want = want || s == src
			}
			if got := one.DMAInt(); got != want {
				t.Errorf("NR$%02X bit %d, source %d: pin %v, want %v", c.reg, c.bit, src, got, want)
			}
		}
	}
}

// CC bit 7 (the NMI term) is stored and read back but maps to no chain index,
// and the three registers keep only the bits the FPGA stores.
func TestNRCCCDCEStoreAndMask(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)
	WireDMAInterruptEnables(disp, NewIM2Driver())
	for _, c := range []struct{ reg, write, read byte }{
		{0xCC, 0xFF, 0x83}, {0xCD, 0xFF, 0xFF}, {0xCE, 0xFF, 0x77},
	} {
		disp.WriteReg(c.reg, c.write)
		if got := disp.ReadReg(c.reg); got != c.read {
			t.Errorf("NR$%02X wrote %02X read %02X, want %02X", c.reg, c.write, got, c.read)
		}
	}
	d := NewIM2Driver()
	disp2 := nextregs.New()
	WirePeripheralMasks(disp2)
	WireDMAInterruptEnables(disp2, d)
	disp2.WriteReg(0xCC, 0x80)
	if d.dmaEnabled != [IM2NumPeriph]bool{} {
		t.Error("CC bit 7 reached a chain index")
	}
}

// A register reset drives every NextReg through a write of zero, so the
// enables follow it back to clear.
func TestNRCCCDCEResetClearsTheEnables(t *testing.T) {
	disp := nextregs.New()
	d := NewIM2Driver()
	WirePeripheralMasks(disp)
	WireDMAInterruptEnables(disp, d)
	disp.WriteReg(0xCD, 0xFF)
	disp.WriteReg(0xCC, 0x03)
	disp.WriteReg(0xCE, 0x77)
	disp.Reset()
	if d.dmaEnabled != [IM2NumPeriph]bool{} {
		t.Errorf("enables survived a reset: %v", d.dmaEnabled)
	}
}

// Wiring order must not matter: the enables follow the registers either way.
func TestWireDMAInterruptEnablesIsOrderIndependent(t *testing.T) {
	disp := nextregs.New()
	d := NewIM2Driver()
	WireDMAInterruptEnables(disp, d)
	WirePeripheralMasks(disp)
	d.SetMode(true)
	d.SetEnabled(IM2SourceULA, true)
	disp.WriteReg(0xCC, 0x01)
	d.Raise(IM2SourceULA)
	d.Lower(IM2SourceULA)
	if !d.DMAInt() {
		t.Fatal("NR$CC bit 0 did not enable the ULA hold when wired in the other order")
	}
}
