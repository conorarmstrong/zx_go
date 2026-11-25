package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NR$1E/$1F read the copper's cvc, which counts from the first paper line, so
// at the frame interrupt they read 248 on a 50 Hz frame (zxnext.vhd:5982-5986,
// video/zxula_timing.vhd). They read the line count from the interrupt until
// 2026-11-24, which put a game's "wait for line 255, then DMA" 248 lines later
// than on hardware and ran Celeste's sprite upload into the next frame
// interrupt. The positions follow the machine timing in force: +3 at power-on.
func TestNR1E1FReadTheCopperLineCounter(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil || emu.nextRegs == nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	d := emu.nextRegs
	d.WriteReg(0x07, 0x00) // 3.5 MHz, so a CPU T-state is a video T-state
	check := func(label string) {
		t.Helper()
		v := next.VideoTimingFor(emu.mem.NextMachineTiming(), false)
		frame := v.Lines * v.HC / 2
		at := func(want int) (msb, lsb byte) {
			for ts := uint64(0); ts < uint64(2*frame); ts++ {
				emu.cpu.SetTstates(ts)
				if emu.ula.VideoTStatesInFrame() == want {
					return d.ReadReg(0x1E), d.ReadReg(0x1F)
				}
			}
			t.Fatalf("%s: no T-state puts the beam at %d", label, want)
			return 0, 0
		}
		paper := v.TFromInt(v.MinVActive, v.MinHActive-12)
		if msb, lsb := at(0); msb != 0 || lsb != 248 {
			t.Errorf("%s: at the frame interrupt NR$1E/$1F = %02X/%02X, want 00/F8 (cvc 248)", label, msb, lsb)
		}
		if msb, lsb := at(paper + 1); msb != 0 || lsb != 0 {
			t.Errorf("%s: on the first paper line NR$1E/$1F = %02X/%02X, want 00/00", label, msb, lsb)
		}
		if msb, lsb := at((paper + 1 + 300*v.HC/2) % frame); msb != 1 || lsb != 0x2C {
			t.Errorf("%s: paper row 300 NR$1E/$1F = %02X/%02X, want 01/2C (cvc 300)", label, msb, lsb)
		}
	}
	check("+3 timing")
}

// An NR$03 write selects the Next's machine timing, and it takes effect at the
// frame sync, raw (0,0) of the timing in force (eff_nr_03_machine_timing,
// zxnext.vhd:6694-6703). The counters run on from there under the new
// figures, so the frame that contains the sync ends at the new timing's
// interrupt. Everything sized from the line or the frame follows: the frame
// INT's pulse width (:2014-2033), the line interrupt and NR$1E/$1F. NextZXOS
// writes $93 (48K timing) to run a 48K snapshot.
func TestNR03MachineTimingTakesEffectAtTheFrameSync(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil || emu.nextRegs == nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	d := emu.nextRegs
	d.WriteReg(0x07, 0x00)
	d.WriteReg(0x23, 0x00)
	d.WriteReg(0x22, 0x02) // line interrupt on, target 0
	runOneFrameHeadless(emu, roms.ModelNext)
	plus3Line := emu.cpu.LineIntOffsetTstates
	frameEnd := func() uint64 { return emu.cpu.Clock() - emu.cpu.Tstates() }

	d.WriteReg(0x03, 0x93) // timing 001: 48K
	if got := emu.mem.TimingModel(); got != roms.ModelPlus3 {
		t.Fatalf("the write took effect before the frame sync: %v", got)
	}
	before := frameEnd()
	runOneFrameHeadless(emu, roms.ModelNext)
	if got := emu.mem.TimingModel(); got != roms.Model48K {
		t.Fatalf("after the frame sync the timing is %v, want 48K", got)
	}
	// +3 from its interrupt (vc 1, hc 126) to raw (0,1): 141816 - 582 + 1
	// columns; then 48K from (0,1) to its interrupt (vc 0, hc 116): 115
	// columns. Together (141816 - 582 + 116)/2 T-states.
	if got, want := frameEnd()-before, uint64((311*456-(456+126)+116)/2); got != want {
		t.Errorf("the frame with the sync ran %d T, want %d", got, want)
	}
	before = frameEnd()
	runOneFrameHeadless(emu, roms.ModelNext)
	if got := frameEnd() - before; got != 69888 {
		t.Errorf("the next frame ran %d T, want 69888 (48K timing)", got)
	}
	if got := emu.cpu.IntPulseTstates; got != 32 {
		t.Errorf("INT pulse %d T, want 32", got)
	}
	v := next.VideoTimingFor(0x01, false)
	if got, want := emu.cpu.LineIntOffsetTstates, uint64(v.LineIntOffset(0, 0)); got != want || got == plus3Line {
		t.Errorf("line interrupt at %d, want %d (48K timing; +3 had %d)", got, want, plus3Line)
	}
	if got := emu.ula.FrameTStates(); got != 69888 {
		t.Errorf("ULA frame %d, want 69888", got)
	}
}

// NR$05 bit 2 (50/60 Hz) is latched at the same frame sync as NR$03's timing
// (eff_nr_05_5060, zxnext.vhd:6699-6700): the frame that contains the sync
// runs the 50 Hz figures to raw (0,1) and the 60 Hz ones from there to the
// interrupt, and the next frame is a 60 Hz one.
func TestNR05FiftySixtyTakesEffectAtTheFrameSync(t *testing.T) {
	emu, err := newEmulator(roms.ModelNext)
	if err != nil || emu.nextRegs == nil {
		t.Skipf("Next core unavailable: %v", err)
	}
	d := emu.nextRegs
	d.WriteReg(0x07, 0x00)
	runOneFrameHeadless(emu, roms.ModelNext)
	frameEnd := func() uint64 { return emu.cpu.Clock() - emu.cpu.Tstates() }

	d.WriteReg(0x05, d.ReadReg(0x05)|0x04)
	if emu.video60Hz || emu.ula.SixtyHz() {
		t.Fatal("60 Hz took effect before the frame sync")
	}
	before := frameEnd()
	runOneFrameHeadless(emu, roms.ModelNext)
	if !emu.video60Hz || !emu.ula.SixtyHz() || !emu.mem.SixtyHz() {
		t.Fatal("the frame sync did not apply 60 Hz")
	}
	// +3 at 50 Hz from its interrupt (vc 1, hc 126) to (0,1), then +3 at
	// 60 Hz from (0,1) to its interrupt (vc 0, hc 126).
	if got, want := frameEnd()-before, uint64((311*456-(456+126)+126)/2); got != want {
		t.Errorf("the frame with the sync ran %d T, want %d", got, want)
	}
	before = frameEnd()
	runOneFrameHeadless(emu, roms.ModelNext)
	if got := frameEnd() - before; got != 60192 {
		t.Errorf("the next frame ran %d T, want 60192 (60 Hz)", got)
	}
}
