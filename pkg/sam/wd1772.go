package sam

import "bytes"

// WD1772 is the SAM Coupé's floppy disk controller (a WD179x-family part, MFM
// only). It exposes four registers selected by the low two address bits —
// status/command, track, sector, data — and serves sectors from a Disk image.
// Modelled on the pkg/betadisk WD1793 structure with the SAM specifics
// (instantaneous seek, side via the port address, LOST_DATA timeout).
type WD1772 struct {
	disk *Disk

	// stateID distinguishes this drive in a captured machine state; the two
	// drives must not share one. See SetStateID in state.go.
	stateID string

	status  byte
	track   byte
	sector  byte
	data    byte
	cyl     int // physical head position
	side    int
	lastDir int // step direction memory (+1 in, -1 out)

	cmdType1 bool
	buffer   []byte
	bufPos   int
	writing  bool
	// formatting is set while a WRITE TRACK transfer is collecting the
	// host's track image; filling the buffer then commits the format
	// rather than a single sector.
	formatting  bool
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
	// A new command ends any format in progress, committing the part of
	// the track that had already streamed past the head. FORCE INTERRUPT
	// does its own commit below, so exclude it here to avoid doing it
	// twice.
	if f.formatting && cmd>>4 != 0xD {
		f.commitWriteTrack()
	}
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
	case 0xE: // READ TRACK
		f.readTrackCmd()
	case 0xF: // WRITE TRACK (FORMAT)
		f.writeTrackCmd()
	}
}

// writeTrackLen is the number of format bytes the host streams for one
// double-density track. SAMDOS writes a full track image; the parser
// below takes whatever arrives and stops at the end of the buffer, so
// the exact length only has to be generous enough to hold a real one.
const writeTrackLen = 6250

// writeTrackCmd (WD177x $Fx) begins a FORMAT: the host streams a whole
// track of format bytes through DRQ and commitWriteTrack parses them
// into sectors.
//
// This used to fall into a "complete benignly" branch — INTRQ raised, no
// error, not one byte moved — so SAMDOS reported the disk formatted and
// every old sector was still there.
func (f *WD1772) writeTrackCmd() {
	f.cmdType1 = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	if f.disk.WriteProtected() {
		f.endIO(wdWriteProt)
		return
	}
	f.buffer, f.bufPos = make([]byte, writeTrackLen), 0
	f.writing, f.formatting, f.multiSector = true, true, false
	f.status = wdBusy | wdDRQ
	f.drqReads = 0
}

// commitWriteTrack parses the collected format stream and writes each
// sector's data into the image.
//
// The SAM image is a flat geometry store, so a format cannot move a
// sector or change its size: what it does is lay fresh data over the
// track, which is exactly what SAMDOS's FORMAT is for. An ID whose
// cylinder/sector falls outside the image's geometry is skipped rather
// than folded somewhere else.
func (f *WD1772) commitWriteTrack() {
	f.formatting = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	for _, sec := range parseFormatStream(f.buffer) {
		if sec.data == nil {
			continue // ID-only record: nothing to write
		}
		f.disk.WriteSector(f.cyl, f.side, int(sec.r), sec.data)
	}
	f.endIO(0)
}

// formatSector is one record recovered from a WRITE TRACK stream.
type formatSector struct {
	c, h, r, n byte
	data       []byte
}

// parseFormatStream extracts sectors from a WD177x WRITE TRACK byte
// stream: an $FE ID address mark is followed by C,H,R,N; the next $FB
// data address mark is followed by 128<<N data bytes. $F5-$F7 are the
// controller's "write a special byte / write the CRC" codes and carry no
// payload, so they are simply passed over by the scan.
func parseFormatStream(stream []byte) []formatSector {
	var sectors []formatSector
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
		// Find this sector's data address mark, giving up if a new ID
		// arrives first (an ID-only sector).
		for i < len(stream) && stream[i] != 0xFB && stream[i] != 0xFE {
			i++
		}
		if i >= len(stream) || stream[i] != 0xFB {
			sectors = append(sectors, formatSector{c: c, h: h, r: r, n: n})
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
		sectors = append(sectors, formatSector{c: c, h: h, r: r, n: n, data: data})
	}
	return sectors
}

// readTrackCmd (WD177x $Ex) hands the host a whole track image. The
// SAM image stores sectors rather than flux, so the track is
// synthesised with the same IDAM/DAM framing a format writes — which is
// what makes a READ TRACK / WRITE TRACK round trip work.
func (f *WD1772) readTrackCmd() {
	f.cmdType1 = false
	if f.disk == nil {
		f.endIO(wdRNF)
		return
	}
	_, _, spt, ss := f.disk.Geometry()
	sizeCode := byte(0)
	for s := ss; s > 128; s >>= 1 {
		sizeCode++
	}
	var image []byte
	image = append(image, bytes.Repeat([]byte{0x4E}, 60)...) // post-index gap
	for sec := 1; sec <= spt; sec++ {
		data, ok := f.disk.ReadSector(f.cyl, f.side, sec)
		if !ok {
			continue
		}
		image = append(image, bytes.Repeat([]byte{0x00}, 12)...)
		image = append(image, 0xFE, byte(f.cyl), byte(f.side), byte(sec), sizeCode)
		image = append(image, 0xF7)                              // CRC
		image = append(image, bytes.Repeat([]byte{0x4E}, 22)...) // ID gap
		image = append(image, bytes.Repeat([]byte{0x00}, 12)...)
		image = append(image, 0xFB)
		image = append(image, data...)
		image = append(image, 0xF7)                              // CRC
		image = append(image, bytes.Repeat([]byte{0x4E}, 54)...) // data gap
	}
	f.buffer, f.bufPos, f.writing, f.multiSector = image, 0, false, false
	f.status = wdBusy | wdDRQ
	f.drqReads = 0
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
	// A FORCE INTERRUPT during a WRITE TRACK terminates the format. On
	// the real part each sector is written as it streams past the head,
	// so whatever arrived before the abort is already on the disk: commit
	// what we collected rather than discarding it.
	if f.formatting {
		f.commitWriteTrack()
	}
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
		if f.formatting {
			f.commitWriteTrack()
			return
		}
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
