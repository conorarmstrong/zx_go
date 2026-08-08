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
	fdc     fdc
	control byte
	ram     [WinTop - WinBase + 1]byte
}

// New returns an Opus Discovery with no disks mounted.
func New() *Device { return &Device{} }

// Mount inserts a disk into a drive (0 or 1).
func (d *Device) Mount(drive int, img *Image) {
	if drive >= 0 && drive < len(d.fdc.disks) {
		d.fdc.disks[drive] = img
	}
}

// SetWriteProtect marks a drive's disk read-only.
func (d *Device) SetWriteProtect(drive int, wp bool) {
	if drive >= 0 && drive < len(d.fdc.wprot) {
		d.fdc.wprot[drive] = wp
	}
}

// Control returns the drive-control latch as last written.
func (d *Device) Control() byte { return d.control }

// TryRead reads an interface address, reporting whether the address was ours.
func (d *Device) TryRead(addr uint16) (byte, bool) {
	switch Decode(addr) {
	case RegionFDC:
		switch FDCRegister(addr) {
		case 0:
			return d.fdc.status, true
		case 1:
			return d.fdc.track, true
		case 2:
			return d.fdc.sector, true
		default:
			return d.fdc.readData(), true
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
			d.fdc.writeCommand(v)
		case 1:
			d.fdc.track = v
		case 2:
			d.fdc.sector = v
		default:
			d.fdc.writeData(v)
		}
		return true
	case RegionControl:
		d.control = v
		// The low bits select the drive. The Opus is single-sided, so there
		// is no side line to derive.
		d.fdc.drive = int(v & 0x01)
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
