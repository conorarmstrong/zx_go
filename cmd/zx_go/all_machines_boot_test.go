package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Every classic machine must cold-boot to its interactive prompt: a
// non-blank screen (the © copyright / 128-menu) and the CPU running
// (not halted with interrupts off). This is a broad regression net
// across all machine types.
func TestAllClassicMachinesBoot(t *testing.T) {
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	defer func() { cliFlagsActive = prev }()

	for _, m := range []roms.SpectrumModel{
		roms.Model48K, roms.Model128K, roms.ModelPlus2,
		roms.ModelPlus2A, roms.ModelPlus3,
	} {
		t.Run(roms.GetModelName(m), func(t *testing.T) {
			emu, err := newEmulator(m)
			if err != nil {
				t.Fatalf("newEmulator(%s): %v", roms.GetModelName(m), err)
			}
			emu.paused.Store(false)
			for i := 0; i < 200; i++ {
				runOneFrameHeadless(emu, m)
			}
			// Non-blank screen: the bitmap or attribute area carries
			// real content (the boot ROM has drawn its banner).
			if screenIsBlank(emu) {
				t.Errorf("%s: screen blank after boot — ROM didn't draw", roms.GetModelName(m))
			}
			// CPU alive: not parked in HALT with interrupts disabled
			// (that's the dead-machine signature).
			if emu.cpu.Halted && !emu.cpu.IFF1 {
				t.Errorf("%s: CPU dead (HALT, IFF1=false) at PC=$%04X", roms.GetModelName(m), emu.cpu.PC)
			}
		})
	}
}

// screenIsBlank reports whether the ULA display bank (the 6144-byte
// bitmap at $4000 in the active screen page) is entirely zero — a
// machine that booted has drawn its copyright banner.
func screenIsBlank(emu *emulator) bool {
	for addr := uint16(0x4000); addr < 0x5800; addr++ {
		if emu.mem.Read(addr) != 0 {
			return false
		}
	}
	return true
}
