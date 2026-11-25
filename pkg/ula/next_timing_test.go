package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func newNextTimingULA(t *testing.T, nr03 byte) (*ULA, *memory.Memory, *uint64, *nextregs.Dispatcher) {
	t.Helper()
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	var ts uint64
	mem.TStates = &ts
	u := New(mem, keyboard.New())
	d := nextregs.New()
	u.SetNextRegs(d)
	mem.SetNextMachineTiming(nr03)
	mem.LatchVideoTiming()
	mem.GetPage(5)[0] = 0xA4 // first bitmap byte of the screen
	return u, mem, &ts, d
}

// The Next's frame, line and paper timing follow NR$03's machine timing.
func TestNextFrameFollowsMachineTiming(t *testing.T) {
	for _, c := range []struct {
		nr03  byte
		model roms.SpectrumModel
	}{{0x01, roms.Model48K}, {0x02, roms.Model128K}, {0x03, roms.ModelPlus3}, {0x04, roms.ModelPentagon}} {
		u, _, _, _ := newNextTimingULA(t, c.nr03)
		if got, want := u.FrameTStates(), roms.FrameTStatesFor(c.model, false); got != want {
			t.Errorf("timing %d: frame %d, want %d", c.nr03, got, want)
		}
		if got, want := u.tStatesPerLine(), roms.TStatesPerLine(c.model); got != want {
			t.Errorf("timing %d: line %d, want %d", c.nr03, got, want)
		}
		if got, want := u.displayStartTState(), roms.DisplayStartTState(c.model); got != want {
			t.Errorf("timing %d: paper at %d, want %d", c.nr03, got, want)
		}
	}
}

// Port $FF reads the ULA's floating bus on 48K and 128K timing and $FF on the
// others (zxnext.vhd:4513), unless NR$08 bit 2 selects the Timex register
// (:2813). On +3 timing a read with A15:12 = 0 and A1:0 = 01 is the +3
// floating bus while paging is unlocked (:2578, :2589, :4517).
func TestNextFloatingBusFollowsMachineTiming(t *testing.T) {
	for _, c := range []struct {
		nr03           byte
		model          roms.SpectrumModel
		wantFF, wantFD byte
	}{
		{0x01, roms.Model48K, 0xA4, 0xFF},
		{0x02, roms.Model128K, 0xA4, 0xFF},
		{0x03, roms.ModelPlus3, 0xFF, 0xA5}, // bit 0 forced on +3 timing (zxula.vhd:573)
		{0x04, roms.ModelPentagon, 0xFF, 0xFF},
	} {
		u, _, ts, d := newNextTimingULA(t, c.nr03)
		*ts = uint64(roms.DisplayStartTState(c.model) + 2) // bitmap fetch of column 0
		if v, _ := u.ReadPort(0x00FF); v != c.wantFF {
			t.Errorf("timing %d: IN $FF = %#02x, want %#02x", c.nr03, v, c.wantFF)
		}
		for _, p := range []uint16{0x0FFD, 0x0A01} {
			if v, _ := u.ReadPort(p); v != c.wantFD {
				t.Errorf("timing %d: IN $%04X = %#02x, want %#02x", c.nr03, p, v, c.wantFD)
			}
		}
		u.WritePort(0x00FF, 0x06)
		d.Store(0x08, 0x04)
		if v, _ := u.ReadPort(0x00FF); v != 0x06 {
			t.Errorf("timing %d: NR$08 bit 2: IN $FF = %#02x, want the Timex register $06", c.nr03, v)
		}
	}
}

// Between fetches the +3 floating bus is the last CPU access to contended
// memory (zxnext.vhd:4496-4507, zxula.vhd:573), not $FF.
func TestNextPlus3FloatingBusHoldsTheLastContendedAccess(t *testing.T) {
	u, mem, ts, _ := newNextTimingULA(t, 0x03)
	*ts = 100                       // the top border: no fetch
	mem.NoteCPUAccess(0x8000, 0x11) // bank 2: not contended on +3 timing
	if v, _ := u.ReadPort(0x0FFD); v != 0xFF {
		t.Errorf("before any contended access: %#02x, want $FF", v)
	}
	mem.NoteCPUAccess(0x4000, 0x3C) // bank 5: contended
	mem.NoteCPUAccess(0x8000, 0x11)
	if v, _ := u.ReadPort(0x0FFD); v != 0x3C {
		t.Errorf("after a contended access of $3C: %#02x, want $3C", v)
	}
}
