package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Each NR$03 machine timing is a zxula_timing.vhd personality. The display
// start (interrupt to first paper fetch at hc_ula 0) and the frame length it
// implies must agree with pkg/roms, whose figures were cross-checked by
// simulating zxula_timing.vhd under GHDL.
func TestVideoTimingAgreesWithTheSimulatedFigures(t *testing.T) {
	for _, c := range []struct {
		nr03  byte
		sixty bool
		model roms.SpectrumModel
	}{
		{0x00, false, roms.Model48K}, {0x01, false, roms.Model48K},
		{0x02, false, roms.Model128K}, {0x03, false, roms.ModelPlus3},
		{0x04, false, roms.ModelPentagon},
		{0x01, true, roms.Model48K}, {0x02, true, roms.Model128K},
		{0x03, true, roms.ModelPlus3},
	} {
		v := VideoTimingFor(c.nr03, c.sixty)
		if got, want := v.TFromInt(v.MinVActive, v.MinHActive-12), roms.DisplayStartTStateFor(c.model, c.sixty); got != want {
			t.Errorf("timing %d 60Hz=%v: display start %d, want %d", c.nr03, c.sixty, got, want)
		}
		if got, want := v.Lines*v.HC/2, roms.FrameTStatesFor(c.model, c.sixty); got != want {
			t.Errorf("timing %d 60Hz=%v: frame %d, want %d", c.nr03, c.sixty, got, want)
		}
		if got := v.TFromInt(v.IntV, v.IntH); got != 0 {
			t.Errorf("timing %d: the interrupt is at %d T from itself", c.nr03, got)
		}
	}
}

// NR$1E/$1F on each timing: cvc reloads with NR$64 on the first paper line
// and so reads (lines - 1st paper line + interrupt line) at the interrupt.
func TestActiveVideoLineAtTheInterruptPerTiming(t *testing.T) {
	for _, c := range []struct {
		nr03 byte
		want int
	}{
		{0x01, 248}, // 48K: vc 0 at the interrupt, 312 lines, reload at 64
		{0x02, 248}, // 128K: vc 1, 311 lines
		{0x03, 248}, // +3: vc 1, 311 lines
		{0x04, 239}, // Pentagon: vc 319, 320 lines, reload at 80
	} {
		v := VideoTimingFor(c.nr03, false)
		if got := v.ActiveVideoLine(0, 0); got != c.want {
			t.Errorf("timing %d: cvc at the interrupt = %d, want %d", c.nr03, got, c.want)
		}
		// One T-state before the first paper fetch it is still the line
		// before; one after, it has reloaded to 0. (On the Pentagon the
		// fetch is an odd number of 7 MHz columns after the interrupt, so
		// it falls inside a T-state.)
		ds := v.TFromInt(v.MinVActive, v.MinHActive-12)
		if got := v.ActiveVideoLine(ds+1, 0); got != 0 {
			t.Errorf("timing %d: cvc after the first paper fetch = %d, want 0", c.nr03, got)
		}
		if got := v.ActiveVideoLine(ds-1, 0); got != v.Lines-1 {
			t.Errorf("timing %d: cvc just before it = %d, want %d", c.nr03, got, v.Lines-1)
		}
	}
}

// The line interrupt fires at hc_ula 255 of paper row target-1. On 128K timing
// that is 126 T into the line after the interrupt's own line offset, as
// WireLineInterrupt has always computed.
func TestLineIntOffsetMatches128KDerivation(t *testing.T) {
	v := VideoTimingFor(0x02, false)
	for _, target := range []int{0, 1, 100, 310} {
		row := (target + 311 - 1) % 311
		line := (64 + row + 311 - 1) % 311
		if got, want := v.LineIntOffset(target, 0), line*228+126; got != want {
			t.Errorf("target %d: %d, want %d", target, got, want)
		}
	}
}

// The line interrupt compares cvc, which carries NR$64, with target-1
// (zxula_timing.vhd:566-581; i_cu_offset is "applied to ula vertical pixel
// count for copper and line int"). So at the instant it fires, NR$1E/$1F read
// target-1, whatever the offset and the timing.
func TestLineInterruptFiresWhereCVCIsTheTargetLineBefore(t *testing.T) {
	for _, nr03 := range []byte{0x01, 0x02, 0x03, 0x04} {
		v := VideoTimingFor(nr03, false)
		for _, off := range []byte{0, 10, 200} {
			for _, target := range []int{0, 1, 100, 255, v.Lines - 1} {
				at := v.LineIntOffset(target, off)
				want := (target + v.Lines - 1) % v.Lines
				if got := v.ActiveVideoLine(at, off); got != want {
					t.Errorf("timing %d offset %d target %d: cvc %d at the interrupt, want %d", nr03, off, target, got, want)
				}
			}
		}
	}
}
