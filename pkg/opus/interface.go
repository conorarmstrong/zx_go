package opus

import "fmt"

// ROMSize is the size of the Opus Discovery interface ROM.
const ROMSize = 8192

// Interface is a complete Opus Discovery as the machine sees it: the ROM
// overlay, the M1 address-trap pager that switches it, and the hardware in the
// window above the ROM.
//
// While the overlay is active it covers the whole of $0000-$3FFF, replacing
// the Spectrum ROM. That is the entire extent of the interface: it never
// answers for $4000 and up.
type Interface struct {
	Dev   *Device
	pager Pager
	clock func() uint64
	rom   [ROMSize]byte
}

// NewInterface returns an Opus Discovery holding rom, which must be the 8 KB
// interface ROM.
func NewInterface(rom []byte) (*Interface, error) {
	if len(rom) != ROMSize {
		return nil, fmt.Errorf("opus: ROM is %d bytes, want %d", len(rom), ROMSize)
	}
	i := &Interface{Dev: New()}
	copy(i.rom[:], rom)
	i.Reset()
	return i, nil
}

// Reset returns the interface to power-on, with the ROM paged in.
func (i *Interface) Reset() { i.pager.Reset() }

// Active reports whether the Opus ROM is currently over the Spectrum ROM.
func (i *Interface) Active() bool { return i.pager.Active() }

// SetActive forces the overlay state, for save-state restore.
func (i *Interface) SetActive(a bool) { i.pager.SetActive(a) }

// SetClock supplies the CPU's absolute T-state counter, which paces the
// controller's byte transfers.
func (i *Interface) SetClock(f func() uint64) { i.clock = f }

// PreFetch advances the ROM pager for an opcode fetch at pc, and paces the
// controller. Every opcode fetch is an instruction boundary, which is exactly
// where an NMI can be taken, so this is the right granularity for DRQ.
func (i *Interface) PreFetch(pc uint16) {
	if i.clock != nil {
		i.Dev.TickTo(i.clock())
	}
	i.pager.PreFetch(pc)
}

// TryRead reads through the overlay, reporting whether the Opus answered.
func (i *Interface) TryRead(addr uint16) (byte, bool) {
	if !i.pager.Active() || addr > WinTop {
		return 0xFF, false
	}
	if addr <= ROMTop {
		return i.rom[addr], true
	}
	return i.Dev.TryRead(addr)
}

// Read reads through the overlay. Callers that have not checked Active first
// should use TryRead.
func (i *Interface) Read(addr uint16) byte { v, _ := i.TryRead(addr); return v }

// Write writes through the overlay, reporting whether the Opus took it. The
// ROM area is read-only, so a write there is absorbed rather than passed on:
// the Spectrum RAM underneath is not reachable while the overlay is active.
func (i *Interface) Write(addr uint16, v byte) bool {
	if !i.pager.Active() || addr > WinTop {
		return false
	}
	if addr <= ROMTop {
		return true
	}
	return i.Dev.TryWrite(addr, v)
}
