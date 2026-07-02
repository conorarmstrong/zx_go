package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// TestConfigureClassicIntTiming pins the maskable frame-INT model per machine:
// 128K-family models get the hardware-faithful narrow pulse (so an INT whose
// pulse falls inside a DI'd section — e.g. an ISR spanning the frame boundary —
// is MISSED, as on real hardware), while 48K keeps the legacy held-whole-frame
// model. The held model over-fired the INT and drifted timing-sensitive games
// (garbled sprites in Ghouls 'n' Ghosts).
func TestConfigureClassicIntTiming(t *testing.T) {
	newCPU := func(model roms.SpectrumModel) *z80.CPU {
		mem, err := memory.New("roms", model)
		if err != nil {
			t.Fatalf("memory.New(%s): %v", roms.GetModelName(model), err)
		}
		return z80.New(mem, ula.New(mem, nil))
	}

	cases := []struct {
		model        roms.SpectrumModel
		wantPulse    uint64 // 0 = legacy held mode
		wantAssertGT bool   // assert tstate should be set (>0) when pulsed
	}{
		{roms.Model128K, 36, true},
		{roms.ModelPlus2, 36, true},
		{roms.ModelPlus3, 32, true},
		{roms.ModelPlus2A, 32, true},
		{roms.ModelPentagon, 36, true},
		{roms.Model48K, 0, false},
	}
	for _, c := range cases {
		cpu := newCPU(c.model)
		configureClassicIntTiming(cpu, c.model)
		if cpu.IntPulseTstates != c.wantPulse {
			t.Errorf("%s: IntPulseTstates = %d, want %d", roms.GetModelName(c.model), cpu.IntPulseTstates, c.wantPulse)
		}
		if c.wantAssertGT && cpu.IntAssertTstate == 0 {
			t.Errorf("%s: IntAssertTstate = 0, want > 0 (narrow pulse)", roms.GetModelName(c.model))
		}
		if !c.wantAssertGT && cpu.IntAssertTstate != 0 {
			t.Errorf("%s: IntAssertTstate = %d, want 0 (held mode)", roms.GetModelName(c.model), cpu.IntAssertTstate)
		}
	}
}

// TestConfigureClassicIntTimingNext pins that switching to the Spectrum Next via
// the Machine menu (switchModel -> configureClassicIntTiming) installs the
// hardware-faithful narrow frame-INT pulse. The Next boots in +3/128K timing
// (NR$03 "011"). The menu-switch path must not leave the CPU in whatever INT
// mode the previous model used (e.g. 48K's held-whole-frame): a held INT
// re-fires across DI'd ISR boundaries, garbling software sprites.
func TestConfigureClassicIntTimingNext(t *testing.T) {
	// The function only reads the model argument, not CPU memory, so any
	// memory model backs the CPU fine.
	mem, err := memory.New("roms", roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cpu := z80.New(mem, ula.New(mem, nil))
	// Simulate the previous model being 48K (held mode), as switchModel would
	// leave it before this call.
	cpu.IntAssertTstate, cpu.IntPulseTstates = 0, 0

	configureClassicIntTiming(cpu, roms.ModelNext)

	if cpu.IntPulseTstates == 0 {
		t.Fatal("ModelNext: IntPulseTstates = 0 (held mode) — sprites garble; want the narrow pulse")
	}
	if cpu.IntAssertTstate == 0 {
		t.Error("ModelNext: IntAssertTstate = 0, want > 0 (narrow pulse)")
	}
}
