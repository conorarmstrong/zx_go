// Package disciple implements the Miles Gordon Technology DISCiPLE disk
// interface. The DISCiPLE plugs into the Spectrum's edge connector and
// provides a WD1770 floppy disk controller with two drive ports, 8KB
// of ROM (GDOS or G+DOS), and 8KB of RAM. When the DISCiPLE's memory
// is paged in, ROM appears at 0x0000-0x1FFF and RAM at 0x2000-0x3FFF.
//
// Disk images are loaded via LoadDisk and parsed using the plus3fdc
// package's raw-sector parsers (ParseMGT, ParseIMG, ParseSAD). The
// internal representation is FUSE's track byte stream model, which
// lets the WD1770 emulation find sectors by walking IDAMs and DAMs.
//
// Port decode (low byte of address):
//
//	0x1B — WD1770 Status (R) / Command (W)
//	0x3B — WD1770 Track register (R/W)
//	0x5B — WD1770 Sector register (R/W)
//	0x7B — WD1770 Data register (R/W)
//	0x1F — DISCiPLE control register (see controlWrite / controlRead)
//
// The control register at 0x1F conflicts with the Kempston joystick.
// This is historically accurate: on the real Spectrum, the DISCiPLE
// intercepted port 0x1F and Kempston joystick would not work.
package disciple

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Port addresses. The WD1770 registers are selected by bits 6-5 of
// the port low byte when bit 7 is 0 and bit 2 is 0. The control
// register is selected when bit 7 is 0 and bit 2 is 1.
const (
	portFDCCmdStatus = 0x1B // WD1770 Command (W) / Status (R)
	portFDCTrack     = 0x3B // WD1770 Track register
	portFDCSector    = 0x5B // WD1770 Sector register
	portFDCData      = 0x7B // WD1770 Data register
	portControl      = 0x1F // DISCiPLE control / status
)

// WD1770 status register bits.
const (
	stBusy         = 0x01
	stDRQ          = 0x02 // Type II/III: data request
	stIndex        = 0x02 // Type I: index pulse
	stTrack0       = 0x04 // Type I: at track 0
	stLostData     = 0x04 // Type II/III: lost data
	stCRCError     = 0x08
	stSeekError    = 0x10 // Type I: seek error
	stRNF          = 0x10 // Type II/III: record not found
	stHeadLoaded   = 0x20 // Type I
	stRecordType   = 0x20 // Type II/III: deleted data
	stWriteProtect = 0x40
	stNotReady     = 0x80
)

// Disciple represents the DISCiPLE disk interface.
type Disciple struct {
	// ROM/RAM
	rom []byte // 8KB GDOS ROM
	ram []byte // 8KB RAM

	// WD1770 registers
	statusReg byte
	trackReg  byte
	sectorReg byte
	dataReg   byte

	// WD1770 transfer state
	xferBuf   []byte // sector data buffer
	xferPos   int    // current byte position in transfer
	xferLen   int    // expected transfer length
	xferWrite bool   // true = writing to disk
	busy      bool
	drq       bool  // data request
	intrq     bool  // interrupt request
	lastCmdType1 bool // true if last command was Type I (affects status format)

	// Drive/head state
	drive int // 0 or 1
	head  int // 0 or 1

	// Disk images
	disks     [2]*plus3fdc.Disk
	diskPaths [2]string

	// Control register (last written value)
	controlReg byte

	// Interface state
	enabled   bool
	inhibited bool
	romPaged  bool

	// Memory reference
	memory *memory.Memory
}

// NewDisciple creates a new DISCiPLE interface.
func NewDisciple(romPath string, memory *memory.Memory) (*Disciple, error) {
	d := &Disciple{
		ram:    make([]byte, 0x2000),
		memory: memory,
	}
	if err := d.loadROM(romPath); err != nil {
		return nil, fmt.Errorf("failed to load Disciple ROM: %w", err)
	}
	d.reset()
	return d, nil
}

// loadROM loads the GDOS ROM.
func (d *Disciple) loadROM(romPath string) error {
	romFiles := []string{"gdos.rom", "disciple.rom", "GDOS_3d.rom"}
	for _, filename := range romFiles {
		if data, err := os.ReadFile(filepath.Join(romPath, filename)); err == nil && len(data) == 0x2000 {
			d.rom = data
			return nil
		}
		if data, err := roms.ReadEmbeddedROM(filename); err == nil && len(data) == 0x2000 {
			d.rom = data
			return nil
		}
	}
	d.rom = make([]byte, 0x2000)
	copy(d.rom[0:], []byte{0xF3, 0xC3, 0x00, 0x20}) // DI, JP 0x2000
	log.Println("Warning: Using placeholder GDOS ROM - disk functionality will be limited")
	return nil
}

// reset resets the DISCiPLE to initial state.
func (d *Disciple) reset() {
	d.statusReg = 0
	d.trackReg = 0
	d.sectorReg = 1
	d.dataReg = 0
	d.xferBuf = nil
	d.xferPos = 0
	d.xferLen = 0
	d.xferWrite = false
	d.busy = false
	d.drq = false
	d.intrq = false
	d.lastCmdType1 = true
	d.drive = 0
	d.head = 0
	d.controlReg = 0
	d.enabled = true
	d.inhibited = false
	d.romPaged = false
}

// HandlePortRead handles reads from DISCiPLE I/O ports.
func (d *Disciple) HandlePortRead(port uint16) (byte, bool) {
	if d.inhibited || !d.enabled {
		return 0, false
	}
	switch port & 0xFF {
	case portFDCCmdStatus:
		d.intrq = false
		return d.readStatus(), true
	case portFDCTrack:
		return d.trackReg, true
	case portFDCSector:
		return d.sectorReg, true
	case portFDCData:
		return d.readData(), true
	case portControl:
		return d.controlRead(), true
	}
	return 0, false
}

// HandlePortWrite handles writes to DISCiPLE I/O ports.
func (d *Disciple) HandlePortWrite(port uint16, value byte) bool {
	if d.inhibited || !d.enabled {
		return false
	}
	switch port & 0xFF {
	case portFDCCmdStatus:
		d.executeCommand(value)
		return true
	case portFDCTrack:
		d.trackReg = value
		return true
	case portFDCSector:
		d.sectorReg = value
		return true
	case portFDCData:
		d.writeData(value)
		return true
	case portControl:
		d.controlWrite(value)
		return true
	}
	return false
}

// controlWrite handles a write to the DISCiPLE control register.
//
//	Bit 0: Drive 1 select
//	Bit 1: Drive 2 select
//	Bit 2: Side select (0 = side 0, 1 = side 1)
//	Bit 3: Density (ignored — we always use MFM)
//	Bit 4: DISCiPLE ROM/RAM page control (1 = paged in)
func (d *Disciple) controlWrite(val byte) {
	d.controlReg = val
	if val&0x02 != 0 {
		d.drive = 1
	} else {
		d.drive = 0
	}
	d.head = int((val >> 2) & 1)
	if val&0x10 != 0 {
		d.romPaged = true
	} else {
		d.romPaged = false
	}
}

// controlRead returns the control register read value.
//
//	Bit 6: INTRQ from WD1770
//	Bit 7: DRQ from WD1770
func (d *Disciple) controlRead() byte {
	var val byte
	if d.intrq {
		val |= 0x40
	}
	if d.drq {
		val |= 0x80
	}
	return val
}

// readStatus builds the WD1770 status register. The bit layout differs
// between Type I and Type II/III commands.
func (d *Disciple) readStatus() byte {
	var st byte
	disk := d.disks[d.drive]

	if disk == nil {
		st |= stNotReady
	}

	if d.busy {
		st |= stBusy
	}

	if d.lastCmdType1 {
		// Type I status
		if disk != nil {
			st |= stHeadLoaded
		}
		if d.trackReg == 0 {
			st |= stTrack0
		}
	} else {
		// Type II/III status
		if d.drq {
			st |= stDRQ
		}
	}

	// Preserve error bits from statusReg
	st |= d.statusReg & (stCRCError | stRNF | stSeekError | stRecordType | stWriteProtect)

	return st
}

// readData returns the next byte from the FDC data register during a
// read operation, advancing the transfer buffer position.
func (d *Disciple) readData() byte {
	if d.drq && d.xferBuf != nil && d.xferPos < d.xferLen {
		val := d.xferBuf[d.xferPos]
		d.xferPos++
		if d.xferPos >= d.xferLen {
			d.drq = false
			d.busy = false
			d.intrq = true
		}
		d.dataReg = val
		return val
	}
	return d.dataReg
}

// writeData accepts the next byte from the CPU during a write operation.
func (d *Disciple) writeData(val byte) {
	d.dataReg = val
	if d.drq && d.xferWrite && d.xferBuf != nil && d.xferPos < d.xferLen {
		d.xferBuf[d.xferPos] = val
		d.xferPos++
		if d.xferPos >= d.xferLen {
			d.commitWriteSector()
			d.drq = false
			d.busy = false
			d.intrq = true
		}
	}
}

// executeCommand dispatches a WD1770 command.
func (d *Disciple) executeCommand(cmd byte) {
	// Clear error bits from previous command.
	d.statusReg = 0

	switch {
	case cmd&0xF0 == 0x00: // Restore
		d.lastCmdType1 = true
		d.trackReg = 0
		d.intrq = true

	case cmd&0xF0 == 0x10: // Seek
		d.lastCmdType1 = true
		d.trackReg = d.dataReg
		d.intrq = true

	case cmd&0xE0 == 0x20: // Step
		d.lastCmdType1 = true
		d.intrq = true

	case cmd&0xE0 == 0x40: // Step-In
		d.lastCmdType1 = true
		if d.trackReg < 79 {
			d.trackReg++
		}
		d.intrq = true

	case cmd&0xE0 == 0x60: // Step-Out
		d.lastCmdType1 = true
		if d.trackReg > 0 {
			d.trackReg--
		}
		d.intrq = true

	case cmd&0xE0 == 0x80: // Read Sector
		d.lastCmdType1 = false
		d.cmdReadSector(cmd)

	case cmd&0xE0 == 0xA0: // Write Sector
		d.lastCmdType1 = false
		d.cmdWriteSector(cmd)

	case cmd&0xF0 == 0xC0: // Read Address
		d.lastCmdType1 = false
		d.cmdReadAddress()

	case cmd&0xF0 == 0xD0: // Force Interrupt
		d.busy = false
		d.drq = false
		d.xferBuf = nil
		d.intrq = true
		d.lastCmdType1 = true

	case cmd&0xF0 == 0xE0: // Read Track (not implemented)
		d.lastCmdType1 = false
		d.statusReg |= stRNF
		d.intrq = true

	case cmd&0xF0 == 0xF0: // Write Track (not implemented)
		d.lastCmdType1 = false
		d.statusReg |= stRNF
		d.intrq = true
	}
}

// cmdReadSector implements the WD1770 Read Sector command. It finds the
// sector matching sectorReg on the current track and loads its data
// into the transfer buffer for byte-by-byte reading via the data
// register.
func (d *Disciple) cmdReadSector(cmd byte) {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, int(d.trackReg))
	if tr == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	sec := tr.FindSector(d.sectorReg)
	if sec == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	d.xferBuf = sec.Data
	d.xferLen = len(sec.Data)
	d.xferPos = 0
	d.xferWrite = false
	d.busy = true
	d.drq = true
}

// cmdWriteSector implements the WD1770 Write Sector command. It sets up
// an empty buffer for the CPU to fill via data register writes; when
// full, the sector is committed to the disk image.
func (d *Disciple) cmdWriteSector(cmd byte) {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, int(d.trackReg))
	if tr == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	// Look up the sector to determine its size, then allocate a write
	// buffer. The actual commit happens in commitWriteSector once the
	// buffer is full.
	sec := tr.FindSector(d.sectorReg)
	if sec == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	d.xferBuf = make([]byte, len(sec.Data))
	d.xferLen = len(sec.Data)
	d.xferPos = 0
	d.xferWrite = true
	d.busy = true
	d.drq = true
}

// commitWriteSector writes the transfer buffer back to the track's
// byte stream, overwriting the data field of the target sector.
func (d *Disciple) commitWriteSector() {
	disk := d.disks[d.drive]
	if disk == nil {
		return
	}
	tr := disk.Track(d.head, int(d.trackReg))
	if tr == nil {
		return
	}
	// Walk the track to find the matching IDAM, then locate its data
	// field and overwrite the bytes. This writes directly into the
	// Track's internal byte stream.
	pos := 0
	for {
		_, _, r, _, idEnd, ok := tr.IdAt(pos)
		if !ok {
			break
		}
		if r == d.sectorReg {
			ds, _, dok := tr.DataAt(idEnd)
			if dok {
				tr.WriteData(ds, d.xferBuf)
			}
			return
		}
		pos = idEnd
	}
}

// cmdReadAddress implements the WD1770 Read Address command. It reads
// the first IDAM on the current track and returns its 6-byte contents
// (C, H, R, N, CRC1, CRC2) via the data register.
func (d *Disciple) cmdReadAddress() {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, int(d.trackReg))
	if tr == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	c, h, r, n, _, ok := tr.IdAt(0)
	if !ok {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	d.xferBuf = []byte{c, h, r, n, 0, 0} // CRC bytes not critical
	d.xferLen = 6
	d.xferPos = 0
	d.xferWrite = false
	d.busy = true
	d.drq = true
	d.trackReg = c // WD1770 updates track register to match
}

// LoadDisk loads a disk image into the specified drive.
func (d *Disciple) LoadDisk(drive int, path string) error {
	if drive < 0 || drive > 1 {
		return fmt.Errorf("invalid drive: %d", drive)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("disciple: read %s: %w", path, err)
	}
	disk, err := parseDiskImage(path, data)
	if err != nil {
		return fmt.Errorf("disciple: parse %s: %w", path, err)
	}
	d.disks[drive] = disk
	d.diskPaths[drive] = path
	return nil
}

// parseDiskImage dispatches to the correct parser based on file
// extension, falling back to signature detection.
func parseDiskImage(path string, data []byte) (*plus3fdc.Disk, error) {
	// Try signature-based detection first (covers DSK, EDSK, UDI, SAD).
	if disk, err := plus3fdc.ParseDiskImage(data); err == nil {
		return disk, nil
	}
	// Extension fallback for headerless raw-sector formats.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mgt":
		return plus3fdc.ParseMGT(data)
	case ".img", ".opd":
		return plus3fdc.ParseIMG(data)
	case ".trd":
		return plus3fdc.ParseTRD(data)
	case ".d40", ".d80":
		return plus3fdc.ParseD40D80(data)
	}
	// Last resort: try MGT (most common DISCiPLE format).
	return plus3fdc.ParseMGT(data)
}

// EjectDisk removes the disk from the specified drive.
func (d *Disciple) EjectDisk(drive int) {
	if drive >= 0 && drive <= 1 {
		d.disks[drive] = nil
		d.diskPaths[drive] = ""
	}
}

// IsEnabled reports whether the DISCiPLE is enabled and not inhibited.
func (d *Disciple) IsEnabled() bool {
	return d.enabled && !d.inhibited
}

// IsInhibited reports whether the DISCiPLE is inhibited.
func (d *Disciple) IsInhibited() bool {
	return d.inhibited
}

// IsROMPaged reports whether the DISCiPLE ROM/RAM is paged in.
func (d *Disciple) IsROMPaged() bool {
	return d.romPaged
}

// GetROM returns the 8KB GDOS ROM.
func (d *Disciple) GetROM() []byte {
	return d.rom
}

// GetRAM returns the 8KB RAM.
func (d *Disciple) GetRAM() []byte {
	return d.ram
}

// SetInhibit sets the inhibit state.
func (d *Disciple) SetInhibit(inhibit bool) {
	d.inhibited = inhibit
	if inhibit {
		d.romPaged = false
	}
}

// HasDisk reports whether a disk is loaded in the given drive.
func (d *Disciple) HasDisk(drive int) bool {
	if drive < 0 || drive > 1 {
		return false
	}
	return d.disks[drive] != nil
}

// DiskPath returns the path of the disk loaded in the given drive.
func (d *Disciple) DiskPath(drive int) string {
	if drive < 0 || drive > 1 {
		return ""
	}
	return d.diskPaths[drive]
}

// Page-in trigger addresses. The DISCiPLE hardware monitors the
// address bus during M1 (opcode fetch) cycles and automatically
// pages in its ROM/RAM when the CPU fetches from these addresses:
//
//   - 0x0066: NMI handler — triggered by the DISCiPLE's snapshot
//     button (mapped to F12 in the emulator).
//   - 0x0008: RST 8 — the Spectrum's error/command handler. GDOS
//     intercepts this to add disk commands (CAT, LOAD, SAVE, etc.)
//     to BASIC.
const (
	PageInNMI  uint16 = 0x0066
	PageInRST8 uint16 = 0x0008
)

// PreFetchHook is the callback the Z80 should invoke before each
// opcode fetch. Pages the DISCiPLE ROM/RAM in when PC matches one
// of the auto-paging trigger addresses.
func (d *Disciple) PreFetchHook(pc uint16) {
	if d.inhibited || !d.enabled {
		return
	}
	if pc == PageInNMI || pc == PageInRST8 {
		d.romPaged = true
	}
}

// PostFetchHook is provided for symmetry with the IF1 hook
// interface. The DISCiPLE pages out via the control register
// (port 0x1F, bit 4 = 0) rather than an address trigger, so
// this is a no-op.
func (d *Disciple) PostFetchHook(pc uint16) {
	// No page-out trigger — GDOS pages itself out via the
	// control register.
}
