// Package disciple implements the Miles Gordon Technology DISCiPLE disk
// interface. The DISCiPLE plugs into the Spectrum's edge connector and
// provides a WD1770 floppy disk controller with two drive ports, 8KB
// of ROM (GDOS or G+DOS), and 8KB of RAM. When the DISCiPLE's memory
// is paged in, ROM appears at 0x0000-0x1FFF and RAM at 0x2000-0x3FFF
// (or swapped via the boot port).
//
// Disk images are loaded via LoadDisk and parsed using the plus3fdc
// package's raw-sector parsers (ParseMGT, ParseIMG, ParseSAD). The
// internal representation is FUSE's track byte stream model, which
// lets the WD1770 emulation find sectors by walking IDAMs and DAMs.
//
// Port decode (low byte of address), matching FUSE's disciple.c:
//
//	0x1B — WD1770 Status (R) / Command (W)
//	0x5B — WD1770 Track register (R/W)
//	0x9B — WD1770 Sector register (R/W)
//	0xDB — WD1770 Data register (R/W)
//	0x1F — Control register (W) / Joystick-printer status (R)
//	0x7B — Boot memswap (R = normal, W = swapped ROM/RAM)
//	0xBB — Patch page (R = page in, W = page out)
//
// The control register at 0x1F conflicts with the Kempston joystick.
// This is historically accurate.
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

// Port addresses matching FUSE's disciple.c.
const (
	portFDCCmdStatus = 0x1B // WD1770 Command (W) / Status (R)
	portFDCTrack     = 0x5B // WD1770 Track register
	portFDCSector    = 0x9B // WD1770 Sector register
	portFDCData      = 0xDB // WD1770 Data register
	portControl      = 0x1F // Control (W) / Joystick-printer status (R)
	portBoot         = 0x7B // Boot memswap (R=normal, W=swapped)
	portPatch        = 0xBB // Patch page (R=page in, W=page out)
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
	rom []byte // 8KB GDOS ROM
	ram []byte // 8KB RAM

	// WD1770 registers
	statusReg byte
	trackReg  byte
	sectorReg byte
	dataReg   byte

	// WD1770 transfer state
	xferBuf      []byte
	xferPos      int
	xferLen      int
	xferWrite    bool
	busy         bool
	drq          bool
	intrq        bool
	lastCmdType1 bool

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
	memswap   bool // true = ROM/RAM positions swapped

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
	copy(d.rom[0:], []byte{0xF3, 0xC3, 0x00, 0x20})
	log.Println("Warning: Using placeholder GDOS ROM - disk functionality will be limited")
	return nil
}

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
	d.memswap = false
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
	case portBoot:
		d.memswap = false
		return 0, true
	case portPatch:
		d.romPaged = true
		return 0, true
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
	case portBoot:
		d.memswap = true
		return true
	case portPatch:
		d.romPaged = false
		return true
	}
	return false
}

// controlWrite handles a write to the DISCiPLE control register.
// Matches FUSE's disciple_cn_write:
//
//	Bit 0: Drive select (1 = drive 0, 0 = drive 1)
//	Bit 1: Side select (0 = side 0, 1 = side 1)
//	Bit 6: Printer strobe (ignored)
func (d *Disciple) controlWrite(val byte) {
	d.controlReg = val
	if val&0x01 != 0 {
		d.drive = 0
	} else {
		d.drive = 1
	}
	d.head = int((val >> 1) & 1)
}

// controlRead returns joystick/printer status. Bit 6 = printer busy
// (0 = busy). We report "not busy" always.
func (d *Disciple) controlRead() byte {
	return 0xFF
}

// readStatus builds the WD1770 status register.
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
		if disk != nil {
			st |= stHeadLoaded
		}
		if d.trackReg == 0 {
			st |= stTrack0
		}
	} else {
		if d.drq {
			st |= stDRQ
		}
	}

	st |= d.statusReg & (stCRCError | stRNF | stSeekError | stRecordType | stWriteProtect)
	return st
}

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

func (d *Disciple) executeCommand(cmd byte) {
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
		d.cmdReadSector()

	case cmd&0xE0 == 0xA0: // Write Sector
		d.lastCmdType1 = false
		d.cmdWriteSector()

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

func (d *Disciple) cmdReadSector() {
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

func (d *Disciple) cmdWriteSector() {
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
	d.xferBuf = make([]byte, len(sec.Data))
	d.xferLen = len(sec.Data)
	d.xferPos = 0
	d.xferWrite = true
	d.busy = true
	d.drq = true
}

func (d *Disciple) commitWriteSector() {
	disk := d.disks[d.drive]
	if disk == nil {
		return
	}
	tr := disk.Track(d.head, int(d.trackReg))
	if tr == nil {
		return
	}
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
	d.xferBuf = []byte{c, h, r, n, 0, 0}
	d.xferLen = 6
	d.xferPos = 0
	d.xferWrite = false
	d.busy = true
	d.drq = true
	d.trackReg = c
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

func parseDiskImage(path string, data []byte) (*plus3fdc.Disk, error) {
	if disk, err := plus3fdc.ParseDiskImage(data); err == nil {
		return disk, nil
	}
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

// IsMemSwapped reports whether ROM and RAM positions are swapped.
func (d *Disciple) IsMemSwapped() bool {
	return d.memswap
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

// PreFetchHook pages in the DISCiPLE when the CPU fetches from
// specific trigger addresses, matching FUSE's z80_ops.c:
//
//	0x0001 — second byte of cold boot (re-page after reset)
//	0x0008 — RST 8 (GDOS intercepts error/command handler)
//	0x0066 — NMI vector (snapshot button)
//	0x028E — GDOS internal re-entry point
//
// No memswap change — GDOS handles memswap itself via port 0x7B.
// GDOS pages itself out via port 0xBB writes when done.
func (d *Disciple) PreFetchHook(pc uint16) {
	if d.inhibited || !d.enabled {
		return
	}
	switch pc {
	case 0x0001, 0x0008, 0x0066, 0x028E:
		d.romPaged = true
	}
}

// PageIn pages in the DISCiPLE ROM/RAM and pre-initializes the RAM
// workspace. Used by the emulator's reboot path to start GDOS cold
// boot from 0x0000.
//
// The real GDOS performs a two-stage init: the cold boot at ROM
// 0x02F2 does minimal hardware setup, then a second stage (reached
// via RST 8 dispatch at ROM 0x02BF) copies ROM code into RAM and
// sets the "initialized" flag. We perform the second stage here so
// GDOS is fully ready after the cold boot without requiring RST 8
// interception.
func (d *Disciple) PageIn() {
	d.romPaged = true
	d.memswap = false
	// Copy ROM[0x0000-0x091E] to RAM, matching the LDIR at ROM 0x02C7:
	//   LD HL, 0x0000; LD DE, 0x2000; LD BC, 0x091F; LDIR
	// Writes to 0x2000+ go to RAM[addr & 0x1FFF], so this populates
	// RAM[0x0000-0x091E] with the NMI handler, RST 8 handler, and
	// other GDOS routines that execute from RAM when memswap is on.
	copy(d.ram[:0x091F], d.rom[:0x091F])
	// Set the "GDOS initialized" flag. The NMI handler at RAM 0x0066
	// checks RAM[0x1DE5] == 0x47 ('G') before proceeding; without
	// this flag, it immediately pages out and returns.
	d.ram[0x1DE5] = 0x47
}

// PostFetchHook is a no-op — GDOS pages itself out via port 0xBB.
func (d *Disciple) PostFetchHook(pc uint16) {}
