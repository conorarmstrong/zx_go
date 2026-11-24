package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NR$1E/$1F read the copper's cvc, which counts from the first paper line, so
// at the frame interrupt they read 248 on a 50 Hz frame and line 255 comes 7
// lines after the interrupt (zxnext.vhd:5982-5986, video/zxula_timing.vhd).
// They read the line count from the interrupt until 2026-11-24, which put a
// game's "wait for line 255, then DMA" 248 lines later than on hardware and
// ran Celeste's sprite upload into the next frame interrupt.
func TestNR1E1FReadTheCopperLineCounter(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil || emu.nextRegs == nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	d := emu.nextRegs
	d.WriteReg(0x07, 0x00) // 3.5 MHz, so a CPU T-state is a video T-state
	at := func(v int) (msb, lsb byte) {
		for ts := uint64(0); ts < 2*70908; ts++ {
			emu.cpu.SetTstates(ts)
			if emu.ula.VideoTStatesInFrame() == v {
				return d.ReadReg(0x1E), d.ReadReg(0x1F)
			}
		}
		t.Fatalf("no T-state puts the beam at %d", v)
		return 0, 0
	}
	if msb, lsb := at(0); msb != 0 || lsb != 248 {
		t.Errorf("at the frame interrupt NR$1E/$1F = %02X/%02X, want 00/F8 (cvc 248)", msb, lsb)
	}
	if msb, lsb := at(1594); msb != 0 || lsb != 255 {
		t.Errorf("7 lines after the interrupt NR$1E/$1F = %02X/%02X, want 00/FF (cvc 255)", msb, lsb)
	}
	if msb, lsb := at((14362 + 300*228) % 70908); msb != 1 || lsb != 0x2C {
		t.Errorf("paper row 300 NR$1E/$1F = %02X/%02X, want 01/2C (cvc 300)", msb, lsb)
	}
}
