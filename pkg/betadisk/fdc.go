package betadisk

// WD1793 status-register bits. The meaning of bits 1,2,4,5 depends on the
// command class (Type I head-movement vs Type II/III data-transfer); the
// aliases below name each interpretation.
const (
	statusBusy    = 0x01 // all commands: command in progress
	statusIndex   = 0x02 // Type I: index pulse
	statusDRQ     = 0x02 // Type II/III: data register needs service
	statusTrack0  = 0x04 // Type I: head over track 0
	statusLost    = 0x04 // Type II/III: lost data
	statusCRCErr  = 0x08 // CRC error
	statusSeekErr = 0x10 // Type I: seek error
	statusRNF     = 0x10 // Type II/III: record not found
	statusHeadDmp = 0x20 // Type I: head loaded
	statusWrFault = 0x20 // Type II write: write fault
	statusWrProt  = 0x40 // write commands: write protected
	statusNotRdy  = 0x80 // no disk / motor off
)

// FDC models a WD1793 floppy-disk controller as wired into the Beta Disk
// interface. It is an "I/O-advanced" model: a command is performed when written
// and data transfers advance one byte per data-register access, which is
// exactly the contract TR-DOS relies on (it polls the status register and
// services DRQ in a tight loop). Real-hardware microsecond timing is not
// modelled because TR-DOS never depends on it.
type FDC struct {
	statusReg byte
	trackReg  byte
	sectorReg byte
	dataReg   byte
	cmdType1  bool // whether the last command was Type I (status bit meanings)

	side        int
	drive       int
	intrq       bool
	lastStepDir int // +1 (in) or -1 (out); remembered for a bare Step command

	disks        [4]*Image
	writeProtect [4]bool
	headTrack    [4]int // physical head position per drive

	// Active Type II/III data transfer.
	buf     []byte
	bufPos  int
	writing bool         // true while a Write Sector transfer is in flight
	commit  func([]byte) // called with the completed write buffer
}

// NewFDC returns an idle controller with no disks mounted.
func NewFDC() *FDC { return &FDC{} }

// Reset performs a WD1793 master reset: it aborts any transfer and clears the
// status, leaving the head positions and mounted disks intact (a master reset
// does not move the heads).
func (f *FDC) Reset() {
	f.statusReg = 0
	f.buf = nil
	f.bufPos = 0
	f.writing = false
	f.commit = nil
	f.cmdType1 = false
	f.intrq = false
}

// Mount attaches an image to a drive (0..3). A nil image ejects.
func (f *FDC) Mount(drive int, img *Image) {
	if drive < 0 || drive > 3 {
		return
	}
	f.disks[drive] = img
}

// SetWriteProtect marks a drive's disk read-only.
func (f *FDC) SetWriteProtect(drive int, wp bool) {
	if drive < 0 || drive > 3 {
		return
	}
	f.writeProtect[drive] = wp
}

// SelectDrive chooses the active drive (0..3).
func (f *FDC) SelectDrive(drive int) {
	if drive < 0 || drive > 3 {
		return
	}
	f.drive = drive
}

// SetSide chooses the active head (0 or 1).
func (f *FDC) SetSide(side int) { f.side = side & 1 }

// Intrq reports the interrupt-request line (set at command completion).
func (f *FDC) Intrq() bool { return f.intrq }

// Drq reports the data-request line (a byte is ready / needed).
func (f *FDC) Drq() bool { return f.statusReg&statusDRQ != 0 && !f.cmdType1 }

func (f *FDC) curDisk() *Image { return f.disks[f.drive] }

// ReadTrackReg / WriteTrackReg / ReadSectorReg / WriteSectorReg expose the
// track and sector registers (Beta ports $3F and $5F).
func (f *FDC) ReadTrackReg() byte    { return f.trackReg }
func (f *FDC) WriteTrackReg(v byte)  { f.trackReg = v }
func (f *FDC) ReadSectorReg() byte   { return f.sectorReg }
func (f *FDC) WriteSectorReg(v byte) { f.sectorReg = v }

// ReadStatus returns the status register (Beta port $1F read). Reading status
// clears INTRQ, matching the WD1793.
func (f *FDC) ReadStatus() byte {
	f.intrq = false
	return f.statusReg
}

// WriteCommand issues a command (Beta port $1F write).
func (f *FDC) WriteCommand(cmd byte) {
	f.intrq = false
	switch {
	case cmd&0xF0 == 0x00: // Restore
		f.typeISeek(0)
	case cmd&0xF0 == 0x10: // Seek (to track in data register)
		f.typeISeek(int(f.dataReg))
	case cmd&0xE0 == 0x20: // Step
		f.typeIStep(cmd, f.lastStepDir)
	case cmd&0xE0 == 0x40: // Step In (towards higher tracks)
		f.lastStepDir = +1
		f.typeIStep(cmd, +1)
	case cmd&0xE0 == 0x60: // Step Out (towards track 0)
		f.lastStepDir = -1
		f.typeIStep(cmd, -1)
	case cmd&0xE0 == 0x80: // Read Sector
		f.readSector()
	case cmd&0xE0 == 0xA0: // Write Sector
		f.writeSector()
	case cmd&0xF0 == 0xC0: // Read Address
		f.readAddress()
	case cmd&0xF0 == 0xD0: // Force Interrupt
		f.forceInterrupt(cmd)
	default:
		// Read Track / Write Track ($E0/$F0): unsupported by TR-DOS in
		// normal use — complete immediately so a stray command can't hang.
		f.endTypeII(0)
	}
}

func (f *FDC) typeISeek(target int) {
	f.cmdType1 = true
	if target < 0 {
		target = 0
	}
	f.headTrack[f.drive] = target
	f.trackReg = byte(target)
	f.finishTypeI()
}

func (f *FDC) typeIStep(cmd byte, dir int) {
	f.cmdType1 = true
	pos := f.headTrack[f.drive] + dir
	if pos < 0 {
		pos = 0
	}
	f.headTrack[f.drive] = pos
	if cmd&0x10 != 0 { // update-track-register flag
		f.trackReg = byte(pos)
	}
	f.finishTypeI()
}

// finishTypeI sets the Type-I completion status (not busy, head loaded, Track-0
// bit reflecting the head position) and raises INTRQ.
func (f *FDC) finishTypeI() {
	var st byte = statusHeadDmp
	if f.curDisk() == nil {
		st = statusNotRdy
	} else if f.headTrack[f.drive] == 0 {
		st |= statusTrack0
	}
	f.statusReg = st
	f.intrq = true
}

func (f *FDC) readSector() {
	f.cmdType1 = false
	disk := f.curDisk()
	if disk == nil {
		f.statusReg = statusNotRdy
		f.intrq = true
		return
	}
	data, err := disk.ReadSector(f.headTrack[f.drive], f.side, int(f.sectorReg))
	if err != nil {
		f.statusReg = statusRNF // record not found
		f.intrq = true
		return
	}
	f.buf = data
	f.bufPos = 0
	f.writing = false
	f.statusReg = statusBusy | statusDRQ
}

func (f *FDC) writeSector() {
	f.cmdType1 = false
	disk := f.curDisk()
	if disk == nil {
		f.statusReg = statusNotRdy
		f.intrq = true
		return
	}
	if f.writeProtect[f.drive] {
		f.statusReg = statusWrProt
		f.intrq = true
		return
	}
	cyl, side, sec := f.headTrack[f.drive], f.side, int(f.sectorReg)
	// Validate the target up-front so an out-of-range write fails as RNF
	// rather than mid-transfer.
	if _, err := disk.ReadSector(cyl, side, sec); err != nil {
		f.statusReg = statusRNF
		f.intrq = true
		return
	}
	f.buf = make([]byte, SectorSize)
	f.bufPos = 0
	f.writing = true
	f.commit = func(b []byte) { _ = disk.WriteSector(cyl, side, sec, b) }
	f.statusReg = statusBusy | statusDRQ
}

func (f *FDC) readAddress() {
	f.cmdType1 = false
	if f.curDisk() == nil {
		f.statusReg = statusNotRdy
		f.intrq = true
		return
	}
	// The 6-byte ID field the WD1793 streams: track, side, sector, length
	// code (01 = 256 bytes), and a two-byte CRC (we report 0 — TR-DOS doesn't
	// verify it on the address read).
	f.buf = []byte{f.trackReg, byte(f.side), 1, 0x01, 0x00, 0x00}
	// Read Address also copies the track byte into the sector register.
	f.sectorReg = f.trackReg
	f.bufPos = 0
	f.writing = false
	f.statusReg = statusBusy | statusDRQ
}

// forceInterrupt implements the WD1793 Type IV command. Per the datasheet:
// if a command is under execution the busy bit is reset and the remaining
// status bits are left unchanged; if the controller is idle the busy bit is
// reset and the status register is updated to reflect Type I status (head
// loaded, Track 0, Not Ready). In both cases any in-flight data transfer is
// aborted and INTRQ is raised.
func (f *FDC) forceInterrupt(byte) {
	busy := f.statusReg&statusBusy != 0
	f.buf = nil
	f.bufPos = 0
	f.writing = false
	f.commit = nil
	if busy {
		// Terminate the running command: clear Busy (and the data-request line
		// that belonged to a Type II/III transfer); leave the rest unchanged.
		f.statusReg &^= statusBusy | statusDRQ
	} else {
		// Idle: status now reflects the Type I command class.
		f.cmdType1 = true
		f.finishTypeI()
	}
	f.intrq = true
}

// endTypeII clears Busy/DRQ, sets the given residual status bits, ends any
// transfer, and raises INTRQ.
func (f *FDC) endTypeII(extra byte) {
	f.cmdType1 = false
	f.buf = nil
	f.writing = false
	f.statusReg = extra
	f.intrq = true
}

// ReadData returns the next byte of a read transfer (Beta port $7F read).
func (f *FDC) ReadData() byte {
	if f.writing || f.buf == nil || f.bufPos >= len(f.buf) {
		return f.dataReg
	}
	f.dataReg = f.buf[f.bufPos]
	f.bufPos++
	if f.bufPos >= len(f.buf) {
		f.endTypeII(0) // transfer complete
	} else {
		f.statusReg = statusBusy | statusDRQ
	}
	return f.dataReg
}

// WriteData feeds the next byte of a write transfer (Beta port $7F write), or
// just latches the data register when no write is in flight (e.g. before a Seek).
func (f *FDC) WriteData(v byte) {
	f.dataReg = v
	if !f.writing || f.buf == nil || f.bufPos >= len(f.buf) {
		return
	}
	f.buf[f.bufPos] = v
	f.bufPos++
	if f.bufPos >= len(f.buf) {
		if f.commit != nil {
			f.commit(f.buf)
		}
		f.endTypeII(0)
	} else {
		f.statusReg = statusBusy | statusDRQ
	}
}
