// Package opus implements the Opus Discovery disk interface.
//
// The Opus Discovery is MEMORY-mapped rather than port-mapped, which is why
// its ROM contains almost no IN/OUT instructions: the floppy controller and
// the drive-control latch appear as ordinary memory in the $2000-$3FFF window
// that the interface pages in alongside its 8 KB ROM at $0000-$1FFF.
//
// The map below is derived from the published v2.15 ROM disassembly at
// speccy4ever.speccy.org (rom/opus/opus215disas.rtf), cross-checked against
// the v2.22 ROM vendored at roms/opus.rom. The disassembly's own section
// labels ("page-in", "page-out", "port-2") and the set of addresses its
// instructions reach in $2000-$3FFF give the layout; no GPL implementation
// was consulted.
//
// The only genuine port instructions in the ROM are IN A,($FE) for the
// keyboard and IN A,($1F) for the interface's Kempston-compatible joystick
// port. The latter is confirmed real by its context: it reads $3000, tests
// bit 7, and only then reads the joystick.
package opus

import "github.com/conorarmstrong/zx_go/pkg/betadisk"

// Region identifies which part of the interface an address falls in.
type Region int

const (
	// RegionNone means the address is not the Opus interface's at all.
	RegionNone Region = iota
	// RegionROM is the 8 KB Opus ROM at $0000-$1FFF.
	RegionROM
	// RegionRAM is the interface's own RAM in the paged window.
	RegionRAM
	// RegionFDC is the WD1770 register file.
	RegionFDC
	// RegionControl is the drive-control and status latch.
	RegionControl
)

// Address map. Four consecutive WD1770 registers and a small control area,
// both inside the $2000-$3FFF window.
const (
	ROMBase  = 0x0000
	ROMTop   = 0x1FFF
	FDCBase  = 0x2800
	FDCTop   = 0x2803
	CtrlBase = 0x3000
	CtrlTop  = 0x3003
	WinBase  = 0x2000
	WinTop   = 0x3FFF
)

// Decode reports which region of the interface an address belongs to.
func Decode(addr uint16) Region {
	switch {
	case addr <= ROMTop:
		return RegionROM
	case addr >= FDCBase && addr <= FDCTop:
		return RegionFDC
	case addr >= CtrlBase && addr <= CtrlTop:
		return RegionControl
	case addr >= WinBase && addr <= WinTop:
		return RegionRAM
	default:
		return RegionNone
	}
}

// FDCRegister maps an FDC address to its WD1770 register index:
// 0 = command/status, 1 = track, 2 = sector, 3 = data.
func FDCRegister(addr uint16) int { return int(addr-FDCBase) & 3 }

// Device is one Opus Discovery interface.
//
// The floppy controller is pkg/betadisk's Western Digital FD179x/177x
// implementation: the WD1770 the Opus uses shares that command set, so the
// controller is reused rather than reimplemented.
type Device struct {
	fdc     *betadisk.FDC
	control byte
	ram     [WinTop - WinBase + 1]byte
}

// New returns an Opus Discovery with no disks mounted.
func New() *Device {
	d := &Device{fdc: &betadisk.FDC{}}
	d.fdc.Reset()
	return d
}

// FDC exposes the controller so a host can mount images on it.
func (d *Device) FDC() *betadisk.FDC { return d.fdc }

// Control returns the drive-control latch as last written.
func (d *Device) Control() byte { return d.control }

// TryRead reads an interface address, reporting whether the address was ours.
func (d *Device) TryRead(addr uint16) (byte, bool) {
	switch Decode(addr) {
	case RegionFDC:
		switch FDCRegister(addr) {
		case 0:
			return d.fdc.ReadStatus(), true
		case 1:
			return d.fdc.ReadTrackReg(), true
		case 2:
			return d.fdc.ReadSectorReg(), true
		default:
			return d.fdc.ReadData(), true
		}
	case RegionControl:
		return d.control, true
	case RegionRAM:
		return d.ram[addr-WinBase], true
	}
	return 0xFF, false
}

// TryWrite writes an interface address, reporting whether it was ours.
func (d *Device) TryWrite(addr uint16, v byte) bool {
	switch Decode(addr) {
	case RegionFDC:
		switch FDCRegister(addr) {
		case 0:
			d.fdc.WriteCommand(v)
		case 1:
			d.fdc.WriteTrackReg(v)
		case 2:
			d.fdc.WriteSectorReg(v)
		default:
			d.fdc.WriteData(v)
		}
		return true
	case RegionControl:
		d.control = v
		// Low bits select the drive; the WD1770 has no side line of its own,
		// so the side comes from the same latch.
		d.fdc.SelectDrive(int(v & 0x03))
		d.fdc.SetSide(int(v>>4) & 1)
		return true
	case RegionRAM:
		d.ram[addr-WinBase] = v
		return true
	}
	return false
}

// Read is TryRead for callers that have already decoded the address.
func (d *Device) Read(addr uint16) byte { v, _ := d.TryRead(addr); return v }

// Write is TryWrite for callers that have already decoded the address.
func (d *Device) Write(addr uint16, v byte) { d.TryWrite(addr, v) }
