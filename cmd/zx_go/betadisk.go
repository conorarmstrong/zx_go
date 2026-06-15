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
// (0..3), enabling the Beta interface if it isn't already.
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
	return nil
}

// ejectTRD removes the disk in the given Beta drive.
func (e *emulator) ejectTRD(drive int) {
	if e.betaDisk != nil {
		e.betaDisk.FDC.Mount(drive, nil)
	}
}
