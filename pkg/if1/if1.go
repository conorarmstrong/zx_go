package if1

import (
	"errors"
	"fmt"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// ROMSize is the size of the Interface 1 shadow ROM. The IF1 ships
// with an 8 KB ROM that overlays the Spectrum's 16 KB ROM area when
// paged in — see if1.c:159 (#define ROM_SIZE 0x2000) and the
// memory_map_romcs_8k double-mapping at if1.c:455-461 that mirrors
// the same 8 KB across both halves of the 0x0000-0x3FFF window.
const ROMSize = 0x2000

// ErrROMSize is returned by SetROM / LoadROM when the supplied byte
// slice isn't exactly 8 KB.
var ErrROMSize = errors.New("if1: ROM image must be exactly 8192 bytes")

// IF1 is the top-level Interface 1 device. It owns:
//
//   - the shadow ROM that pages over the Spectrum ROM at 0x0000-0x3FFF
//   - the IF1 ULA that handles port I/O
//   - the microdrive bus the ULA controls
//   - the page-in flag the Z80 ROM trap toggles
//
// Once constructed, the IF1 needs to be wired into the emulator with:
//
//	memory.PeripheralRead = if1.MemoryRead   // ROM overlay
//	ula.HandlePortRead    = if1.HandlePortRead // I/O dispatch
//
// (Or via PeripheralManager — see pkg/peripherals/manager.go.)
//
// The IF1 is intended for the 48K Spectrum only. The 128K, +2, and
// +3 don't have an IF1 connector, and their ROM trap addresses are
// different so the page-in/page-out hooks wouldn't work.
type IF1 struct {
	rom    []byte
	active bool

	ULA *ULA
}

// New constructs a fresh IF1 with no ROM loaded and no cartridges
// inserted. Call LoadROM (or LoadROMBytes) before activating, and
// use the ULA's Bus to insert microdrive cartridges.
func New() *IF1 {
	return &IF1{
		ULA: NewULA(nil),
	}
}

// LoadROM reads an 8 KB Interface 1 ROM image from disk. The standard
// distribution is `if1-2.rom` (Edition 2 of the IF1 ROM), the same
// file FUSE expects via its `rom_interface_1` setting at
// fuse-1.6.0/settings.c:214.
func (i *IF1) LoadROM(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("if1: read %s: %w", path, err)
	}
	return i.LoadROMBytes(data)
}

// LoadROMBytes installs an 8 KB ROM image from a byte slice. Used by
// the embedded-ROM fallback path in pkg/roms.
func (i *IF1) LoadROMBytes(data []byte) error {
	if len(data) != ROMSize {
		return fmt.Errorf("%w: got %d bytes", ErrROMSize, len(data))
	}
	i.rom = make([]byte, ROMSize)
	copy(i.rom, data)
	return nil
}

// HasROM reports whether a valid IF1 ROM has been loaded. The IF1
// can't usefully activate without one — without the shadow ROM,
// none of the BASIC extension keywords (CAT, FORMAT, OPEN #, ...)
// would resolve.
func (i *IF1) HasROM() bool {
	return len(i.rom) == ROMSize
}

// ROM returns a read-only view of the loaded ROM bytes. nil if
// no ROM is loaded.
func (i *IF1) ROM() []byte {
	return i.rom
}

// Active reports whether the IF1 shadow ROM is currently paged in.
// When true, MemoryRead returns IF1 ROM bytes for any address in
// 0x0000-0x3FFF, hiding the Spectrum's main ROM. The HasROM check
// matches MemoryRead's gate so callers can use Active as a single
// "is the overlay live?" predicate without worrying about whether a
// ROM is loaded — the hot-path PreFetchHook intentionally skips the
// ROM check to stay cheap.
func (i *IF1) Active() bool {
	return i.active && i.HasROM()
}

// PageIn maps the IF1 shadow ROM over the Spectrum ROM area. The
// mapping mirrors the same 8 KB chunk into both halves of the
// 0x0000-0x3FFF window, exactly like FUSE's if1_memory_map at
// if1.c:454-461.
//
// Called by the Z80 instruction-fetch trap when the CPU executes
// from one of the IF1 trigger addresses (RST 8 / 0x1708 / similar).
func (i *IF1) PageIn() {
	if !i.HasROM() {
		return
	}
	i.active = true
}

// PageOut unmaps the shadow ROM, restoring access to the main
// Spectrum ROM. Called when the IF1 ROM finishes its current
// command and returns control to BASIC.
func (i *IF1) PageOut() {
	i.active = false
}

// MemoryRead is the PeripheralRead-compatible callback the memory
// package calls for ROM-area addresses. Returns the IF1 ROM byte
// when the IF1 is active, otherwise (0, false) so the caller falls
// through to the host ROM.
//
// The 8 KB ROM is mirrored across 0x0000-0x3FFF to match the
// hardware's bank-switched A13 line — see if1.c:455-461.
func (i *IF1) MemoryRead(addr uint16) (byte, bool) {
	if !i.active || !i.HasROM() {
		return 0, false
	}
	if addr >= 0x4000 {
		return 0, false
	}
	return i.rom[addr&0x1FFF], true
}

// Cartridge returns the cartridge currently in microdrive slot
// `which` (0-based), or nil if the slot is empty or out of range.
// Convenience accessor so callers don't have to walk
// IF1.ULA.Bus.Drive(slot).Cartridge themselves.
func (i *IF1) Cartridge(which int) *microdrive.Cartridge {
	d := i.ULA.Bus.Drive(which)
	if d == nil {
		return nil
	}
	return d.Cartridge
}

// HandlePortRead dispatches an IN instruction to the IF1 ULA. Returns
// (value, true) if the address decoded to an IF1 port. The caller
// should fall through on (_, false).
func (i *IF1) HandlePortRead(port uint16) (byte, bool) {
	return i.ULA.HandlePortRead(port)
}

// HandlePortWrite dispatches an OUT instruction to the IF1 ULA.
// Returns true if the address decoded to an IF1 port.
func (i *IF1) HandlePortWrite(port uint16, val byte) bool {
	return i.ULA.HandlePortWrite(port, val)
}

// Reset clears the IF1's runtime state: ROM is paged out, ULA is
// reset, microdrives stop spinning. The ROM image and any inserted
// cartridges stay in place. Called from the system reset path.
func (i *IF1) Reset() {
	i.active = false
	i.ULA.Reset()
}

// Spectrum-ROM PC values at which the Interface 1 hardware switches
// its shadow ROM in and out. Sourced from FUSE's z80_ops.c:228-289
// (the if1p / if1u CHECK blocks).
const (
	// PageInRST8 is the Spectrum ROM RST 8 error/syntax handler.
	// The IF1 ROM intercepts this so it can extend the BASIC
	// interpreter with new keywords (CAT, FORMAT, OPEN #, ...).
	PageInRST8 uint16 = 0x0008
	// PageInCloseChannel is the close-channel routine — RS-232
	// stream cleanup taps in here too.
	PageInCloseChannel uint16 = 0x1708
	// PageOutAddr is the IF1 ROM exit point. After fetching the
	// byte at this address (which lives in the IF1 ROM), the
	// hardware unmaps the shadow ROM so subsequent fetches go
	// back to the host ROM.
	PageOutAddr uint16 = 0x0700
)

// PreFetchHook is the callback the Z80 should invoke before each
// opcode fetch. Pages the IF1 ROM IN when PC matches one of the
// page-in triggers. The check is inlined to keep the per-instruction
// hot path to two integer compares — at ~4M instructions/sec the
// slice-walk version was a measurable cost.
func (i *IF1) PreFetchHook(pc uint16) {
	if pc == PageInRST8 || pc == PageInCloseChannel {
		i.active = true
	}
}

// PostFetchHook is the callback the Z80 should invoke after each
// opcode fetch but before dispatch. Pages the IF1 ROM OUT when PC
// matches the page-out trigger. The fetched byte still executes —
// only subsequent reads see the host ROM again.
func (i *IF1) PostFetchHook(pc uint16) {
	if pc == PageOutAddr {
		i.active = false
	}
}
