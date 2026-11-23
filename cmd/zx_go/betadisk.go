package main

import (
	"fmt"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/betadisk"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Beta Disk / TR-DOS glue. The interface is created the first time a .TRD is
// mounted: the TR-DOS ROM is installed into memory (armed for $3Dxx
// auto-paging), the WD1793 is wired to the ULA's port dispatch, and a CPU
// pre-fetch hook drives the paging state machine.

// betaSupported reports whether the current machine can run TR-DOS. The Beta
// Disk is a classic-Spectrum interface; it's most at home on the Pentagon (and
// other 128K clones) but also works on the 48K/128K. The Next has its own
// divMMC-based storage, and the ZX80/81 predate it.
func (e *emulator) betaSupported() bool {
	switch e.model {
	case roms.Model48K, roms.Model128K, roms.ModelPlus2, roms.ModelPentagon:
		return true
	}
	return false
}

// ensureBeta lazily constructs and wires the Beta interface, returning it.
func (e *emulator) ensureBeta() (*betadisk.Interface, error) {
	if e.betaDisk != nil {
		return e.betaDisk, nil
	}
	if !e.betaSupported() {
		return nil, fmt.Errorf("TR-DOS / Beta disk is not available for %s", roms.GetModelName(e.model))
	}
	rom, err := e.loadTRDOSROM()
	if err != nil {
		return nil, err
	}
	e.mem.EnableBeta(rom)
	bd := betadisk.NewInterface()
	// Fuse's fdd spins the disk once the Beta motor starts and strobes an
	// index pulse once per revolution; TR-DOS's boot restore wait polls the
	// Type-I status Index bit and cannot advance without that pulse. Wire the
	// CPU's T-states clock so the FDC can phase the strobe (period = 10
	// frames = Fuse's 200ms revolution at 50Hz).
	// cpu.FrameTStates is only populated by the sam/zx8x machines — on the
	// Spectrum family it is zero, which armed idxNextT at "now + 0" and
	// wedged syncIndex in an infinite toggle loop on the first IN $1F
	// after motor spin-up (SIGQUIT stack: betadisk.(*FDC).syncIndex fdc.go:141).
	// Derive the frame length from the model (Fuse fdd.c index events pace on
	// the real frame rate) and scale by turbo like the CPU's own frame budget.
	//
	// The clock must be ABSOLUTE, not cpu.Tstates() handed straight through:
	// ExecuteFrame folds its counter back at every frame boundary
	// (`c.tstates -= tstatesEnd`, pkg/z80/z80.go:1016) — Tstates is
	// frame-relative there, while the single-step path never wraps. The FDC
	// arms idxNextT = now + 19*frameT/2 in absolute units, so against a
	// per-frame-wrapping now the target (≈714k) is never reached: syncIndex
	// never toggles, bit 1 never pulses, and TR-DOS's restore wait polls
	// IN $1F forever. Re-base on
	// every fold — a backward jump in Tstates() is a frame boundary, so
	// advance the base by one frame. Fuse's own tstates is monotonic since
	// machine start; this adapter gives the FDC the same shape without
	// changing the CPU core's frame-relative contract.
	frameT := uint64(roms.FrameTStates(e.model)) * uint64(e.cpu.SpeedMultiplier())
	var absBase, lastRel uint64
	bd.SetClock(func() uint64 {
		rel := uint64(e.cpu.Tstates())
		if rel < lastRel {
			absBase += frameT
		}
		lastRel = rel
		return absBase + rel
	}, frameT)
	e.ula.SetBetaDisk(bd)
	// Drive the $3Dxx in / $4000 out auto-paging from the instruction stream.
	e.cpu.AddPreFetchHook("betadisk", func(pc uint16) { e.mem.BetaPreFetch(pc) })
	e.betaDisk = bd
	return bd, nil
}

// loadTRDOSROM fetches the embedded (or user-supplied) TR-DOS ROM.
func (e *emulator) loadTRDOSROM() ([]byte, error) {
	rm := e.mem.GetROMManager()
	if rom, ok := rm.GetROM(roms.ROMTRDOS); ok {
		return rom, nil
	}
	if err := rm.LoadROM(roms.ROMTRDOS, "trdos.rom"); err != nil {
		return nil, fmt.Errorf("load TR-DOS ROM: %w", err)
	}
	rom, ok := rm.GetROM(roms.ROMTRDOS)
	if !ok {
		return nil, fmt.Errorf("TR-DOS ROM unavailable after load")
	}
	return rom, nil
}

// mountTRD reads a .TRD image from path and mounts it in the given Beta drive
// (0..3), enabling the Beta interface if it isn't already, then runs Fuse's
// autoload law for the disk.
func (e *emulator) mountTRD(drive int, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	img, err := betadisk.LoadImage(data)
	if err != nil {
		return err
	}
	bd, err := e.ensureBeta()
	if err != nil {
		return err
	}
	bd.FDC.Mount(drive, img)

	// Fuse's ui_drive_autoload (beta.c:403-417), run on every .trd mount
	// with autoload on (Fuse's default and the only mode here):
	//
	//	machine_reset(1);
	//	if ((caps & LIBSPECTRUM_MACHINE_CAPABILITY_128_MEMORY) ||
	//	    !settings_current.beta128_48boot) {
	//	  PC = 0;
	//	  machine_current->ram.last_byte |= 0x10;   /* Select ROM 1 */
	//	  beta_page();
	//	}
	//
	// The $7FFD bit4 write is the load-bearing part: it latches
	// ram.current_rom (Fuse spec128_select_rom, spec128.c:139), which is
	// the NOT_128_TYPE_OR_IS_48_TYPE gate that makes the $3Dxx trap live
	// (z80_ops.c:176,183-190). Without it the trap can never page the
	// partner OUT at $4000+, the boot workspace at $5CC0 gets seeded from
	// the pinned TR-DOS ROM instead of the BASIC ROM bytes at $15B9, and
	// the partner glue spins in its step-out retry loop forever.
	// 48K hosts skip the block exactly like Fuse (beta128_48boot default
	// true): they boot plain 48 BASIC and the trap pages TR-DOS in only
	// on the wider $3C00-$3DFF fetch window.
	switch e.model {
	case roms.Model128K, roms.ModelPlus2, roms.ModelPentagon:
		e.reboot() // Fuse machine_reset(1) — clears AY/keys, re-runs boot
		p7, _, _ := e.mem.GetPortState()
		e.mem.PageMemory(p7 | 0x10) // last_byte |= 0x10: select ROM 1
		e.mem.SetBetaActive(true)   // beta_page(): partner paged over $0000
		e.cpu.PC = 0                // PC = 0
	}
	return nil
}

// ejectTRD removes the disk in the given Beta drive.
func (e *emulator) ejectTRD(drive int) {
	if e.betaDisk != nil {
		e.betaDisk.FDC.Mount(drive, nil)
	}
}
