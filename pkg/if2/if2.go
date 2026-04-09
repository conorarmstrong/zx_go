// Package if2 implements the Sinclair Interface 2 ROM cartridge
// slot. The Interface 2 was a small edge-connector peripheral that
// plugged into the 48K Spectrum and accepted 16KB ROM cartridges.
// When a cartridge was inserted, it asserted ROMCS and replaced the
// internal BASIC ROM at 0x0000-0x3FFF with the cartridge contents
// until the user powered off and removed the cartridge.
//
// The Interface 2 was 48K-only. On 128K/+2/+2A/+3 machines the
// cartridge slot isn't reachable because those models' own ROM
// paging uses the same address space. Only about a dozen cartridges
// were ever released commercially — Jet Pac, Pssst!, Tranz Am,
// Cookie (Ultimate), Hungry Horace, Horace and the Spiders, Horace
// Goes Skiing, Chess, Backgammon, Planetoids, Space Raiders.
//
// This package is deliberately minimal: a Cartridge is just the raw
// 16KB ROM image and a display name. The peripheral manager handles
// the actual Read dispatch via HandleMemoryRead, and main.go wires
// the File menu items that insert / eject cartridges and trigger
// the CPU reset needed to boot the cartridge code.
package if2

import (
	"fmt"
	"os"
	"path/filepath"
)

// CartridgeSize is the fixed size of an Interface 2 ROM cartridge
// in bytes. Every cartridge is exactly 16384 bytes — the same as
// one Spectrum ROM bank — and is loaded at 0x0000-0x3FFF.
const CartridgeSize = 16384

// Cartridge is a loaded Interface 2 ROM cartridge.
type Cartridge struct {
	data [CartridgeSize]byte
	Name string
}

// Load reads a cartridge from the filesystem. The file must be
// exactly 16384 bytes; any other size returns an error. The
// returned Cartridge has Name set to the file's basename (without
// extension) so the UI can show it in menus and status bars.
func Load(path string) (*Cartridge, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("if2: read %s: %w", path, err)
	}
	if len(raw) != CartridgeSize {
		return nil, fmt.Errorf("if2: %s has invalid size: %d bytes (expected %d)", path, len(raw), CartridgeSize)
	}
	c := &Cartridge{Name: nameFromPath(path)}
	copy(c.data[:], raw)
	return c, nil
}

// Read returns the byte at addr within the cartridge. The addr is
// masked to the cartridge size so the compiler can elide the slice
// bounds check — callers on the hot path (PeripheralManager.
// HandleMemoryRead) already guard with addr < 0x4000 before getting
// here, so the mask is a no-op for the expected input range.
func (c *Cartridge) Read(addr uint16) byte {
	return c.data[addr&(CartridgeSize-1)]
}

// nameFromPath returns the file's basename with its extension
// stripped. "/path/to/jetpac.rom" → "jetpac".
func nameFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext != "" {
		base = base[:len(base)-len(ext)]
	}
	return base
}
