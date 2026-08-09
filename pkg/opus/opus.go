// Package opus implements the Opus Discovery disk interface.
//
// The Opus Discovery is MEMORY-mapped rather than port-mapped, which is why
// its ROM contains almost no IN/OUT instructions: the floppy controller and
// the drive-control latch appear as ordinary memory in the $2000-$3FFF window
// that the interface pages in alongside its 8 KB ROM at $0000-$1FFF.
//
// The map is taken from the complete v2.2 shadow-ROM disassembly published on
// World of Spectrum, which is the same ROM version vendored at roms/opus.rom,
// and every address below is one the ROM's own instructions reach:
//
//	LD (2800),A  "Pass the command directly to the hardware"
//	LD A,(2800)  "Is the diskdrive still doing I/O?"  BIT 0 = BUSY
//	LD (2801),A  "Pass it [the track register] to the hardware"
//	LD (2802),A  "Signal: go to sector 'A'"
//	LD (2803),A  "Give byte to hardware" / LD A,(2803) "get byte from hardware"
//
// No GPL implementation was consulted.
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

// Address map, decoded from A13/A12/A11 within the paged window. The ROM only
// ever touches $2000-$27FF, $2800-$2803 and $3000-$3003, so the mirroring
// above each block is unobservable to it; the three-line decode is the
// simplest that covers the window without gaps.
const (
	ROMBase = 0x0000
	ROMTop  = 0x1FFF
	// RAMBase..RAMTop is the 2 KB IC 6116 SRAM the ROM copies its tables
	// into. INIT_RAM2 checks for it and records the result in the PIA.
	RAMBase = 0x2000
	RAMTop  = 0x27FF
	// FDCBase..FDCTop is the WD1770, four registers mirrored across the block.
	FDCBase = 0x2800
	FDCTop  = 0x2FFF
	// PIABase..PIATop is the 6821, four registers mirrored across the block.
	PIABase = 0x3000
	PIATop  = 0x3FFF

	// WinBase..WinTop is the whole hardware window above the ROM.
	WinBase = 0x2000
	WinTop  = 0x3FFF

	// RAMSize is the 6116's capacity.
	RAMSize = RAMTop - RAMBase + 1
)

// Decode reports which region of the interface an address belongs to.
func Decode(addr uint16) Region {
	switch {
	case addr <= ROMTop:
		return RegionROM
	case addr <= RAMTop:
		return RegionRAM
	case addr <= FDCTop:
		return RegionFDC
	case addr <= PIATop:
		return RegionControl
	default:
		return RegionNone
	}
}

// FDCRegister maps an FDC address to its WD1770 register index:
// 0 = command/status, 1 = track, 2 = sector, 3 = data.
func FDCRegister(addr uint16) int { return int(addr) & 3 }

// PIARegister maps a control address to its 6821 register index:
// 0 = PRA/DDRA, 1 = CRA, 2 = PRB/DDRB, 3 = CRB.
func PIARegister(addr uint16) int { return int(addr) & 3 }

// Device is one Opus Discovery interface.
//
// The floppy controller is pkg/betadisk's Western Digital FD179x/177x
// implementation: the WD1770 the Opus uses shares that command set, so the
// controller is reused rather than reimplemented.
type Device struct {
	fdc fdc
	pia pia
	ram [RAMSize]byte
}

// New returns an Opus Discovery with no disks mounted.
func New() *Device { return &Device{} }

// TickTo advances the interface's clock to an absolute CPU T-state count,
// handing over the next byte of a transfer once its byte-period has elapsed.
func (d *Device) TickTo(now uint64) { d.fdc.tickTo(now) }

// SetNMI installs the interrupt line the WD1770's DRQ output drives. The Opus
// has no DMA: the ROM's $0066 handler moves one byte per NMI.
func (d *Device) SetNMI(f func()) { d.fdc.nmi = f }

// Mount inserts a disk into a drive (0 or 1).
func (d *Device) Mount(drive int, img *Image) {
	if drive >= 0 && drive < len(d.fdc.disks) {
		d.fdc.disks[drive] = img
	}
}

// Disk returns the image mounted in a drive, or nil.
func (d *Device) Disk(drive int) *Image {
	if drive < 0 || drive >= len(d.fdc.disks) {
		return nil
	}
	return d.fdc.disks[drive]
}

// SetWriteProtect marks a drive's disk read-only.
func (d *Device) SetWriteProtect(drive int, wp bool) {
	if drive >= 0 && drive < len(d.fdc.wprot) {
		d.fdc.wprot[drive] = wp
	}
}

// SelectedDrive reports which drive the PIA's port A currently selects. Disk
// subtable byte 2 documents the lines: "bit 0: SET for righthand drive;
// bit 1: SET for lefthand drive".
func (d *Device) SelectedDrive() int {
	if d.pia.portA()&0x01 != 0 {
		return 0
	}
	if d.pia.portA()&0x02 != 0 {
		return 1
	}
	return d.fdc.drive
}

// SelectedSide reports the side line, port A bit 4.
func (d *Device) SelectedSide() int { return int(d.pia.portA()>>4) & 1 }

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
		return d.pia.read(PIARegister(addr)), true
	case RegionRAM:
		return d.ram[addr-RAMBase], true
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
		d.pia.write(PIARegister(addr), v)
		d.fdc.drive = d.SelectedDrive()
		return true
	case RegionRAM:
		d.ram[addr-RAMBase] = v
		return true
	}
	return false
}

// Read is TryRead for callers that have already decoded the address.
func (d *Device) Read(addr uint16) byte { v, _ := d.TryRead(addr); return v }

// Write is TryWrite for callers that have already decoded the address.
func (d *Device) Write(addr uint16, v byte) { d.TryWrite(addr, v) }
