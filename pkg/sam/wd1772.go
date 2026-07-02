package sam

// WD1772 is the SAM Coupé's floppy disk controller (a WD179x-family part, MFM
// only). It exposes four registers selected by the low two address bits —
// status/command, track, sector, data — and serves sectors from a Disk image.
// Modelled on the pkg/betadisk WD1793 structure with the SAM specifics
// (instantaneous seek, side via the port address, LOST_DATA timeout).
type WD1772 struct {
	disk *Disk

	status  byte
	track   byte
	sector  byte
	data    byte
	cyl     int // physical head position
	side    int
	lastDir int // step direction memory (+1 in, -1 out)

	cmdType1    bool
	buffer      []byte
	bufPos      int
	writing     bool
	multiSector bool
	drqReads    int // consecutive DRQ-pending status reads (LOST_DATA timeout)
	intrq       bool
	idxCounter  int // Type I status reads, for the periodic index pulse
}

// WD179x status bits (meaning is command-type dependent).
const (
	wdBusy       = 0x01
	wdDRQ        = 0x02 // Type II/III
	wdIndex      = 0x02 // Type I
	wdLostData   = 0x04 // Type II/III
	wdTrack00    = 0x04 // Type I
	wdCRCError   = 0x08
	wdRNF        = 0x10 // Type II/III: record not found
	wdSeekError  = 0x10 // Type I
	wdWriteFault = 0x20 // Type II/III
	wdSpinUp     = 0x20 // Type I: motor spin-up complete
	wdWriteProt  = 0x40
	wdNotReady   = 0x80 // bit 7: drive not ready (no disk); on the 1772 also "motor on"

	wdLostDataReads = 16 // DRQ unserviced for this many status reads → LOST_DATA
)

// NewWD1772 returns an idle controller with no disk.
func NewWD1772() *WD1772 { return &WD1772{lastDir: 1} }

// InsertDisk loads a disk; Eject removes it.
func (f *WD1772) InsertDisk(d *Disk) { f.disk = d }
func (f *WD1772) Eject()             { f.disk = nil }
func (f *WD1772) HasDisk() bool      { return f.disk != nil }

// SetSide selects the head (0/1) — driven from the SAM port address bit 2.
func (f *WD1772) SetSide(side int) { f.side = side & 1 }

// Intrq reports the interrupt-request line state.
func (f *WD1772) Intrq() bool { return f.intrq }

// Register accessors. Register = port & 3 (0=status/command, 1=track, 2=sector,
// 3=data).
func (f *WD1772) ReadTrack() byte    { return f.track }
func (f *WD1772) WriteTrack(v byte)  { f.track = v }
func (f *WD1772) ReadSector() byte   { return f.sector }
func (f *WD1772) WriteSector(v byte) { f.sector = v }

// WriteCommand issues a controller command.
func (f *WD1772) WriteCommand(cmd byte) {
	f.intrq = false
	switch cmd >> 4 {
	case 0x0: // RESTORE
		f.seekTo(0)
	case 0x1: // SEEK (target in data register)
		f.seekTo(int(f.data))
	case 0x2, 0x3: // STEP (last direction)
		f.step(cmd, f.lastDir)
	case 0x4, 0x5: // STEP-IN
		f.step(cmd, +1)
	case 0x6, 0x7: // STEP-OUT
		f.step(cmd, -1)
	case 0x8, 0x9: // READ SECTOR
		f.readSector(cmd)
	case 0xA, 0xB: // WRITE SECTOR
		f.writeSectorCmd(cmd)
	case 0xC: // READ ADDRESS
		f.readAddress()
	case 0xD: // FORCE INTERRUPT
		f.forceInterrupt()
	default: // 0xE READ TRACK / 0xF WRITE TRACK — complete benignly
		f.cmdType1 = false
		f.endIO(0)
	}
}

func (f *WD1772) seekTo(target int) {
	f.cmdType1 = true
	if target < 0 {
		target = 0
	}
	f.cyl = target
	f.track = byte(target)
	f.finishTypeI()
}

func (f *WD1772) step(cmd byte, dir int) {
	f.cmdType1 = true
	f.lastDir = dir
	f.cyl += dir
	if f.cyl < 0 {
		f.cyl = 0
	}
	if cmd&0x10 != 0 { // update flag
		f.track = byte(f.cyl)
	}
	f.finishTypeI()
}

// finishTypeI completes a positioning command with no seek delay and raises
// INTRQ. The live Type I status bits (TRACK00, SPIN_UP, INDEX, NOT-READY) are
// applied in ReadStatus so they reflect the current disk/head state at read
// time, as on the WD1772.
func (f *WD1772) finishTypeI() {
	f.status = 0
	f.intrq = true
}

func (f *WD1772) sectorSize() int {
	if f.disk == nil {
		return 512
	}
	_, _, _, ss := f.disk.Geometry()
	return ss
}

func (f *WD1772) readSector(cmd byte) {
	f.cmdType1 = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	data, ok := f.disk.ReadSector(f.cyl, f.side, int(f.sector))
	if !ok {
		f.endIO(wdRNF)
		return
	}
	f.buffer, f.bufPos, f.writing = data, 0, false
	f.multiSector = cmd&0x10 != 0
	f.status = wdBusy | wdDRQ
	f.drqReads = 0
}

func (f *WD1772) writeSectorCmd(cmd byte) {
	f.cmdType1 = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	if f.disk.WriteProtected() {
		f.endIO(wdWriteProt)
		return
	}
	if _, ok := f.disk.ReadSector(f.cyl, f.side, int(f.sector)); !ok {
		f.endIO(wdRNF)
		return
	}
	f.buffer, f.bufPos, f.writing = make([]byte, f.sectorSize()), 0, true
	f.multiSector = cmd&0x10 != 0
	f.status = wdBusy | wdDRQ
	f.drqReads = 0
}

func (f *WD1772) readAddress() {
	f.cmdType1 = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	sizecode := byte(0)
	for s := f.sectorSize(); s > 128; s >>= 1 {
		sizecode++
	}
	// ID field: track, side, sector, size code, 2 CRC bytes.
	f.buffer = []byte{byte(f.cyl), byte(f.side), f.sector, sizecode, 0, 0}
	f.bufPos, f.writing, f.multiSector = 0, false, false
	f.status = wdBusy | wdDRQ
	f.drqReads = 0
}

func (f *WD1772) forceInterrupt() {
	// FORCE INTERRUPT terminates any command and reverts the status register to
	// Type I reporting (head position / spin-up), as on the WD1772. SAMDOS reads
	// the status here after loading the DOS to confirm the disk is still ready.
	f.status = 0
	f.writing = false
	f.cmdType1 = true
	f.intrq = true
}

func (f *WD1772) endIO(extra byte) {
	f.status = extra
	f.writing = false
	f.intrq = true
}

// ReadData returns the next byte of a Type II/III transfer.
func (f *WD1772) ReadData() byte {
	if f.status&wdDRQ == 0 || f.writing || f.bufPos >= len(f.buffer) {
		return f.data
	}
	f.data = f.buffer[f.bufPos]
	f.bufPos++
	f.drqReads = 0
	if f.bufPos >= len(f.buffer) {
		if f.multiSector {
			f.sector++
			if data, ok := f.disk.ReadSector(f.cyl, f.side, int(f.sector)); ok {
				f.buffer, f.bufPos = data, 0
			} else {
				f.endIO(0)
			}
		} else {
			f.endIO(0)
		}
	}
	return f.data
}

// WriteData feeds the next byte of a Type II write transfer. When the command
// was issued with the multiple-record flag (WRITE SECTOR MULTIPLE), filling
// the buffer commits the current sector and continues into the next one
// instead of ending the command, mirroring ReadData's multi-sector read.
func (f *WD1772) WriteData(val byte) {
	f.data = val
	if f.status&wdDRQ == 0 || !f.writing {
		return
	}
	f.buffer[f.bufPos] = val
	f.bufPos++
	f.drqReads = 0
	if f.bufPos >= len(f.buffer) {
		f.disk.WriteSector(f.cyl, f.side, int(f.sector), f.buffer)
		if f.multiSector {
			f.sector++
			if _, ok := f.disk.ReadSector(f.cyl, f.side, int(f.sector)); ok {
				f.buffer, f.bufPos = make([]byte, f.sectorSize()), 0
				return
			}
		}
		f.endIO(0)
	}
}

// ReadStatus returns the status register, applying the LOST_DATA timeout (a
// Type II/III transfer whose DRQ goes unserviced) and the not-ready bit.
func (f *WD1772) ReadStatus() byte {
	if !f.cmdType1 && f.status&wdDRQ != 0 {
		f.drqReads++
		if f.drqReads >= wdLostDataReads {
			f.endIO(wdLostData)
		}
	}
	s := f.status
	if f.cmdType1 {
		// Type I live status: head position, motor spin-up and a periodic index
		// pulse while a disk spins; not-ready when the drive is empty. The SAM
		// boot ROM checks SPIN_UP after a RESTORE to confirm a disk is present.
		if f.cyl == 0 {
			s |= wdTrack00
		}
		if f.disk != nil {
			s |= wdSpinUp
			if f.disk.WriteProtected() {
				s |= wdWriteProt
			}
			f.idxCounter++
			if f.idxCounter%4 == 0 {
				s |= wdIndex
			}
		} else {
			s |= wdNotReady
		}
	} else if f.disk == nil {
		s |= wdNotReady
	}
	return s
}
