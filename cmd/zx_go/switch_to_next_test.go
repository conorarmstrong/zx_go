package main

import (
	"errors"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// nextROMsInstalled reports whether the NextZXOS distro ROM is
// present, so Next boot tests can skip cleanly on a bare checkout.
func nextROMsInstalled() bool {
	_, err := install.LoadROM(install.DistroROM)
	return !errors.Is(err, install.ErrROMNotInstalled)
}

// Reproduces the GUI "Machine → ZX Spectrum Next" runtime switch. A
// failure to boot here shows as a black border with a collage of
// flashing coloured boxes — the TBBLUE testcard, i.e. the bootrom
// never ran / Layer-2 or tilemap left enabled with stale bank data.
//
// The switch path is NOT the cold `--next` path: it goes
// mem.SwitchModel(Next) → wireNextSubsystems → reboot(), starting
// from a different model's memory + ULA state. This pins that it
// reaches the NextZXOS welcome screen the same as a cold boot.
func bootReachesWelcome(t *testing.T, emu *emulator) bool {
	t.Helper()
	// Boot to the welcome era (~2400 frames at 28 MHz). The welcome
	// screen fills the ULA bitmap with text; the testcard leaves
	// most of bank 5 as a fixed colour-bar attr pattern. We detect
	// "made progress" by the CPU reaching the NextZXOS HALT-idle PC
	// ($0C90) — the welcome/menu idle — rather than spinning in the
	// bootrom or a NOP-slide.
	for i := 0; i < 2600; i++ {
		runOneFrameHeadless(emu, roms.ModelNext)
	}
	// $0C90 = the NextZXOS DI;HALT idle. A testcard-stuck machine
	// sits in the bootrom ($0000-$3FFF) or a wedged loop instead.
	return emu.cpu.PC == 0x0C90 && emu.cpu.IFF1
}

func TestSwitchToNextFromClassicReachesWelcome(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	defer func() { cliFlagsActive = prev }()

	for _, startModel := range []roms.SpectrumModel{
		roms.Model48K, roms.Model128K, roms.ModelPlus3,
	} {
		t.Run(roms.GetModelName(startModel), func(t *testing.T) {
			emu, err := newEmulator(startModel)
			if err != nil {
				t.Fatalf("newEmulator(%s): %v", roms.GetModelName(startModel), err)
			}
			// Classic-bus peripherals (DISCiPLE + Multiface) enabled in
			// the classic session. On a runtime switch INTO the Next
			// these must be torn down — DISCiPLE's port $1F shadows the
			// Kempston read the TBBLUE firmware polls during boot,
			// crashing it into the testcard. The cold-boot config
			// restore already gates them off for ModelNext; the
			// runtime switch must do the same.
			if err := emu.peripherals.EnableDisciple("roms"); err == nil {
				dev := emu.peripherals.GetDisciple()
				emu.cpu.PreFetchHook = dev.PreFetchHook
				emu.cpu.PostFetchHook = dev.PostFetchHook
			}
			_ = emu.peripherals.EnableMultiface(configStringToMultifaceVariant("MF3"), "roms")

			// Mirror switchModel's body for entering the Next.
			if err := emu.mem.SwitchModel(roms.ModelNext); err != nil {
				t.Fatalf("SwitchModel(Next): %v", err)
			}
			emu.model = roms.ModelNext
			disableClassicBusPeripherals(emu)
			if err := wireNextSubsystems(emu); err != nil {
				t.Fatalf("wireNextSubsystems: %v", err)
			}
			emu.reboot()
			emu.paused.Store(false)

			if !bootReachesWelcome(t, emu) {
				t.Errorf("switch %s→Next did not reach NextZXOS welcome idle: PC=$%04X IFF1=%v Halted=%v (testcard/stuck)",
					roms.GetModelName(startModel), emu.cpu.PC, emu.cpu.IFF1, emu.cpu.Halted)
			}
		})
	}
}
