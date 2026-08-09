package main

import (
	"fmt"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/opus"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Opus Discovery glue. The interface is created the first time a .OPD is
// mounted: its 8 KB ROM is installed as an overlay over $0000-$3FFF, a CPU
// pre-fetch hook drives the M1 address-trap pager and paces the controller,
// and the WD1770's DRQ line is wired to the Z80's NMI.

// opusSupported reports whether the current machine can take an Opus
// Discovery. The interface traps fixed addresses in the 48K ROM — $0008,
// $0048 and $1708 — and substitutes its own code from the following byte, so
// it is only meaningful with that ROM actually paged at $0000.
func (e *emulator) opusSupported() bool { return e.model == roms.Model48K }

// ensureOpus lazily constructs and wires the interface, returning it.
func (e *emulator) ensureOpus() (*opus.Interface, error) {
	if e.opus != nil {
		return e.opus, nil
	}
	if !e.opusSupported() {
		return nil, fmt.Errorf("the Opus Discovery is a 48K interface and is not available for %s",
			roms.GetModelName(e.model))
	}
	rom, err := roms.ReadEmbeddedROM("opus.rom")
	if err != nil {
		return nil, fmt.Errorf("opus ROM: %w", err)
	}
	iface, err := opus.NewInterface(rom)
	if err != nil {
		return nil, err
	}
	iface.Dev.SetNMI(func() { e.cpu.PendingNMI.Store(true) })
	iface.SetClock(e.cpu.Tstates)
	e.mem.SetOpus(iface)
	e.cpu.AddPreFetchHook("opus", iface.PreFetch)
	e.opus = iface
	return iface, nil
}

// mountOPD inserts a .opd image into an Opus drive (0 = drive 1, 1 = drive 2).
//
// Fitting the interface changes what sits at $0000, and its ROM only gets to
// run its own reset vector at power-on, so the machine is rebooted the first
// time a disk is inserted.
func (e *emulator) mountOPD(drive int, path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	img, err := opus.LoadImage(data)
	if err != nil {
		return false, err
	}
	fresh := e.opus == nil
	iface, err := e.ensureOpus()
	if err != nil {
		return false, err
	}
	iface.Dev.Mount(drive, img)
	if fresh {
		e.rebootLocked()
	}
	return fresh, nil
}

// ejectOPD removes the disk in the given Opus drive.
func (e *emulator) ejectOPD(drive int) {
	if e.opus != nil {
		e.opus.Dev.Mount(drive, nil)
	}
}
