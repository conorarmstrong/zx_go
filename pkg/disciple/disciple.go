// Package disciple implements the Miles Gordon Technology DISCiPLE disk
// interface. The DISCiPLE plugs into the Spectrum's edge connector and
// provides a WD1770 floppy disk controller with two drive ports, 8KB
// of ROM (GDOS or G+DOS), and 8KB of RAM. When the DISCiPLE's memory
// is paged in, ROM appears at 0x0000-0x1FFF and RAM at 0x2000-0x3FFF
// (or swapped via the boot port).
//
// Disk images are loaded via LoadDisk and parsed using the plus3fdc
// package's raw-sector parsers (ParseMGT, ParseIMG, ParseSAD). The
// internal representation is a track byte stream, which lets the
// WD1770 emulation find sectors by walking IDAMs and DAMs exactly as
// the chip would while reading a real disk.
//
// Port decode (low byte of address):
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
	"os"
	"path/filepath"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Port addresses for the DISCiPLE's WD1770 and control registers.
const (
	portFDCCmdStatus = 0x1B // WD1770 Command (W) / Status (R)
	portFDCTrack     = 0x5B // WD1770 Track register
	portFDCSector    = 0x9B // WD1770 Sector register
	portFDCData      = 0xDB // WD1770 Data register
	portControl      = 0x1F // Control (W) / Joystick-printer status (R)
	portBoot         = 0x7B // Boot memswap (R=normal, W=swapped)
	portPatch        = 0xBB // Patch page (R=page in, W=page out)
)

// WD1772 status-register bits. Per the WD1772 datasheet "Status Register
// Summary", the meaning of bits 1,2,4,5 depends on the command class (Type I
// head-movement vs Type II/III data-transfer); the aliases below name each
// interpretation. Two bits differ from the WD1793 (the Beta Disk's chip):
//
//   - bit 7 is Motor On in EVERY command class (the WD1772 has no drive-ready
//     input — pin 19 is the raw Read Data line), NOT "Not Ready".
//   - Type I bit 5 is "Spin-Up complete", NOT "Head Loaded".
//
// An empty drive is detected by Record Not Found on a Type II/III command, not
// by any bit-7 ready indicator.
const (
	stBusy         = 0x01
	stDRQ          = 0x02 // Type II/III: data request
	stIndex        = 0x02 // Type I: index pulse
	stTrack0       = 0x04 // Type I: at track 0
	stLostData     = 0x04 // Type II/III: lost data
	stCRCError     = 0x08
	stSeekError    = 0x10 // Type I: seek error
	stRNF          = 0x10 // Type II/III: record not found
	stSpinUp       = 0x20 // Type I: motor spin-up complete
	stRecordType   = 0x20 // Type II/III: deleted data
	stWriteProtect = 0x40
	stMotorOn      = 0x80 // all command classes: Motor On output
)

// wd1772StepRateMs returns the head stepping-motor rate in milliseconds for the
// Type I rate field r1r0, per the WD1772 datasheet "Flag Summary":
//
//	r1r0 = 00 -> 6 ms,  01 -> 12 ms,  10 -> 2 ms,  11 -> 3 ms
//
// These differ from the WD1793's 3/6/10/15 ms. The I/O-advanced controller
// model performs head movement instantly, so this is supplied for faithfulness
// and any future timing model rather than used to gate the current transfers.
func wd1772StepRateMs(r1r0 byte) int {
	switch r1r0 & 0x03 {
	case 0b00:
		return 6
	case 0b01:
		return 12
	case 0b10:
		return 2
	default: // 0b11
		return 3
	}
}

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
	formatting   bool // true while a Write Track (format) transfer is collecting bytes
	busy         bool
	drq          bool
	intrq        bool
	lastCmdType1 bool
	motorOn      bool // WD1772 Motor On output (status bit 7); set when a command runs
	stepDir      int  // last step direction: +1 (in) / -1 (out) / 0, used by a bare Step
	multiSector  bool // m flag of the in-flight Type II Read/Write command

	// Drive/head state
	drive int // 0 or 1
	head  int // 0 or 1

	// Physical R/W head track position per drive. The WD1772 Track Register
	// (trackReg) is a separate latch that the head movement only updates when
	// the command's update flag (u) is set; the physical position below is what
	// actually selects the cylinder the head is over. Restore/Seek always move
	// the head and update the Track Register together.
	headTrack [2]int

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
	// No usable ROM anywhere (filesystem or embedded). Fail rather
	// than install a placeholder: the old placeholder (`DI; JP
	// $2000` + zeros) executed at the $0001 boot hook and looped
	// forever in empty DISCiPLE RAM — a guaranteed black screen on
	// every cold boot with the interface enabled. A missing ROM
	// must leave the interface OFF (callers log and continue), not
	// brick the machine.
	return fmt.Errorf("no GDOS ROM found (looked for %v in %q and embedded assets)", romFiles, romPath)
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
	d.motorOn = false
	d.drive = 0
	d.head = 0
	d.headTrack = [2]int{}
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

// controlWrite handles a write to the DISCiPLE control register:
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

// readStatus builds the WD1772 status register per the datasheet "Status
// Register Summary". The WD1772 has no drive-ready input, so bit 7 is the Motor
// On output in every command class (never "Not Ready"); an empty drive is
// reported via Record Not Found on Type II/III commands, not here. Type I bit 5
// is "Spin-Up complete" (set once the motor is on), not the WD1793's Head
// Loaded.
func (d *Disciple) readStatus() byte {
	var st byte

	if d.motorOn {
		st |= stMotorOn
	}
	if d.busy {
		st |= stBusy
	}

	if d.lastCmdType1 {
		// Type I status: bit 5 = Spin-Up complete (motor up to speed), bit 2 =
		// Track 0. Both the spin-up sequence and the motor are modelled by the
		// motorOn flag, which the chip raises automatically when a command runs.
		if d.motorOn {
			st |= stSpinUp
		}
		if d.trackReg == 0 {
			st |= stTrack0
		}
	} else if d.drq {
		// Type II/III status: bit 1 = Data Request.
		st |= stDRQ
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
			d.advanceMultiSector()
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
			if d.formatting {
				d.commitWriteTrack()
			} else {
				d.commitWriteSector()
				d.advanceMultiSector()
			}
			d.drq = false
			d.busy = false
			d.intrq = true
		}
	}
}

// advanceMultiSector implements the WD1772 multi-record auto-increment: per the
// datasheet "TYPE II COMMANDS" (m=1), "the Sector Register [is] internally
// updated ... in numerical ascending sequence". After a multi-sector Read or
// Write Sector transfer completes, the Sector Register advances to the next
// sector. The one-shot flag is cleared so a subsequent single-sector command is
// not affected.
func (d *Disciple) advanceMultiSector() {
	if d.multiSector {
		d.sectorReg++
		d.multiSector = false
	}
}

func (d *Disciple) executeCommand(cmd byte) {
	d.statusReg = 0
	// WD1772 datasheet "TYPE I COMMANDS": when a command is received and the MO
	// signal is low, the chip forces Motor On (running the spin-up sequence
	// unless the h flag disables it). MO stays asserted until the device has
	// been idle for several revolutions, which the I/O-advanced model never
	// reaches, so once a command runs the motor is on.
	d.motorOn = true

	switch {
	case cmd&0xF0 == 0x00: // Restore (seek track 0)
		d.lastCmdType1 = true
		d.headTrack[d.drive] = 0
		d.trackReg = 0
		d.intrq = true

	case cmd&0xF0 == 0x10: // Seek (to the track in the Data Register)
		d.lastCmdType1 = true
		d.stepDir = stepDir(int(d.dataReg) - d.headTrack[d.drive])
		d.headTrack[d.drive] = int(d.dataReg)
		d.trackReg = d.dataReg
		d.intrq = true

	case cmd&0xE0 == 0x20: // Step (repeat the last step direction)
		d.lastCmdType1 = true
		d.stepHead(cmd, d.stepDir)
		d.intrq = true

	case cmd&0xE0 == 0x40: // Step-In (toward higher track numbers)
		d.lastCmdType1 = true
		d.stepDir = +1
		d.stepHead(cmd, +1)
		d.intrq = true

	case cmd&0xE0 == 0x60: // Step-Out (toward track 0)
		d.lastCmdType1 = true
		d.stepDir = -1
		d.stepHead(cmd, -1)
		d.intrq = true

	case cmd&0xE0 == 0x80: // Read Sector
		d.lastCmdType1 = false
		d.multiSector = cmd&0x10 != 0 // m flag (bit 4)
		d.cmdReadSector()

	case cmd&0xE0 == 0xA0: // Write Sector
		d.lastCmdType1 = false
		d.multiSector = cmd&0x10 != 0 // m flag (bit 4)
		d.cmdWriteSector()

	case cmd&0xF0 == 0xC0: // Read Address
		d.lastCmdType1 = false
		d.multiSector = false
		d.cmdReadAddress()

	case cmd&0xF0 == 0xD0: // Force Interrupt
		// WD1772 datasheet "TYPE IV COMMANDS": if a command is under execution,
		// only the Busy bit resets — the rest of the status bits, and so the
		// interrupted command's Type I/Type II-III bit meanings, are left alone.
		// Only an IDLE Force Interrupt switches the status to Type I.
		wasBusy := d.busy
		d.busy = false
		d.drq = false
		d.xferBuf = nil
		d.intrq = true
		if !wasBusy {
			d.lastCmdType1 = true
		}
		d.multiSector = false

	case cmd&0xF0 == 0xE0: // Read Track
		d.lastCmdType1 = false
		d.multiSector = false
		d.cmdReadTrack()

	case cmd&0xF0 == 0xF0: // Write Track (format)
		d.lastCmdType1 = false
		d.multiSector = false
		d.cmdWriteTrack()
	}
}

// stepHead moves the physical R/W head one track in the given direction,
// clamping at track 0, and updates the Track Register only when the command's
// update flag (u, bit 4) is set — per the WD1772 datasheet "Flag Summary"
// (u = Update Track Register) and the STEP/STEP-IN/STEP-OUT descriptions.
func (d *Disciple) stepHead(cmd byte, dir int) {
	pos := d.headTrack[d.drive] + dir
	if pos < 0 {
		pos = 0
	}
	d.headTrack[d.drive] = pos
	if cmd&0x10 != 0 { // u flag set → Track Register tracks the head
		d.trackReg = byte(pos)
	}
}

// stepDir clamps a track delta to a single step direction (+1 in, -1 out, or 0).
func stepDir(delta int) int {
	switch {
	case delta > 0:
		return +1
	case delta < 0:
		return -1
	default:
		return 0
	}
}

func (d *Disciple) cmdReadSector() {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, d.headTrack[d.drive])
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

// cmdReadTrack (WD177x $E0) streams the entire raw track byte image to the
// host — gaps, sync, address marks, sector IDs + data, CRCs — used by
// track-level disk copiers and verifiers.
func (d *Disciple) cmdReadTrack() {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, d.headTrack[d.drive])
	if tr == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	d.xferBuf = tr.Bytes()
	d.xferLen = len(d.xferBuf)
	d.xferPos = 0
	d.xferWrite = false
	d.busy = true
	d.drq = true
}

// writeTrackLen is the number of format bytes the host streams for one
// double-density Write Track operation (matches plus3fdc's track size).
const writeTrackLen = 6250

// cmdWriteTrack (WD177x $F0) begins a format: the host streams a full
// track of format bytes, which commitWriteTrack parses into sectors.
func (d *Disciple) cmdWriteTrack() {
	if d.disks[d.drive] == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	d.xferBuf = make([]byte, writeTrackLen)
	d.xferLen = writeTrackLen
	d.xferPos = 0
	d.xferWrite = true
	d.formatting = true
	d.busy = true
	d.drq = true
}

// commitWriteTrack parses the collected format stream into sector IDs +
// data and rebuilds the current track.
func (d *Disciple) commitWriteTrack() {
	d.formatting = false
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		return
	}
	if err := disk.FormatTrack(d.head, d.headTrack[d.drive], parseFormatStream(d.xferBuf)); err != nil {
		d.statusReg |= stRNF
	}
}

// parseFormatStream extracts sectors from a WD177x Write Track format byte
// stream: an 0xFE ID address mark is followed by C,H,R,N; the next 0xFB
// data address mark is followed by 128<<N formatted (filler) bytes.
func parseFormatStream(stream []byte) []plus3fdc.Sector {
	var sectors []plus3fdc.Sector
	i := 0
	for i < len(stream) {
		if stream[i] != 0xFE {
			i++
			continue
		}
		if i+5 > len(stream) {
			break
		}
		c, h, r, n := stream[i+1], stream[i+2], stream[i+3], stream[i+4]
		i += 5
		// Locate this sector's data address mark, stopping if a new IDAM
		// arrives first (an ID-only sector).
		for i < len(stream) && stream[i] != 0xFB && stream[i] != 0xFE {
			i++
		}
		if i >= len(stream) || stream[i] != 0xFB {
			sectors = append(sectors, plus3fdc.Sector{C: c, H: h, R: r, N: n})
			continue
		}
		i++ // skip the data address mark
		size := 128 << (n & 0x07)
		end := i + size
		if end > len(stream) {
			end = len(stream)
		}
		data := make([]byte, size)
		copy(data, stream[i:end])
		i = end
		sectors = append(sectors, plus3fdc.Sector{C: c, H: h, R: r, N: n, Data: data})
	}
	return sectors
}

func (d *Disciple) cmdWriteSector() {
	disk := d.disks[d.drive]
	if disk == nil {
		d.statusReg |= stRNF
		d.intrq = true
		return
	}
	tr := disk.Track(d.head, d.headTrack[d.drive])
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
	tr := disk.Track(d.head, d.headTrack[d.drive])
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
	tr := disk.Track(d.head, d.headTrack[d.drive])
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
	// WD1772 datasheet "Read Address": the Track Address of the ID field is
	// written into the Sector Register so a comparison can be made by the user.
	d.sectorReg = c
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
// specific trigger addresses:
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
