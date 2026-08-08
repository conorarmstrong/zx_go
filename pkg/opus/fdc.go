package opus

// WD1770 floppy controller, as the Opus Discovery presents it.
//
// The WD1770 shares the Western Digital FD179x command set: the command byte's
// top nibble selects the type, Type I commands position the head and Type II
// transfer a sector a byte at a time through the data register while DRQ is
// asserted. Only the commands the Opus ROM issues are modelled; anything else
// completes with no error rather than pretending to do something.
//
// The Opus geometry (40 x 18 x 256, single sided) is why this is not
// pkg/betadisk's controller: that one is built around TR-DOS's 16 sectors per
// track.

// Status register bits (FD179x Type II meanings where they differ).
const (
	StatusBusy           = 0x01
	StatusDRQ            = 0x02
	StatusLostData       = 0x04
	StatusRecordNotFound = 0x10
	StatusWriteProtect   = 0x40
	StatusMotorOn        = 0x80
)

// fdc is the controller state.
type fdc struct {
	status byte
	track  byte
	sector byte
	data   byte

	drive int
	disks [2]*Image
	wprot [2]bool

	// Active Type II transfer.
	buf    []byte
	pos    int
	write  bool
	target struct{ track, sector int }
}

func (f *fdc) disk() *Image {
	if f.drive < 0 || f.drive >= len(f.disks) {
		return nil
	}
	return f.disks[f.drive]
}

// writeCommand starts a command. Type is the top nibble.
func (f *fdc) writeCommand(cmd byte) {
	f.endTransfer()
	switch {
	case cmd&0xF0 == 0x00: // RESTORE
		f.track = 0
		f.status = StatusMotorOn
	case cmd&0xF0 == 0x10: // SEEK — target is in the data register
		f.track = f.data
		f.status = StatusMotorOn
	case cmd&0xE0 == 0x20: // STEP
		f.status = StatusMotorOn
	case cmd&0xE0 == 0x40: // STEP IN
		if int(f.track) < Cylinders-1 {
			f.track++
		}
		f.status = StatusMotorOn
	case cmd&0xE0 == 0x60: // STEP OUT
		if f.track > 0 {
			f.track--
		}
		f.status = StatusMotorOn
	case cmd&0xE0 == 0x80: // READ SECTOR
		f.startRead()
	case cmd&0xE0 == 0xA0: // WRITE SECTOR
		f.startWrite()
	case cmd&0xF0 == 0xD0: // FORCE INTERRUPT
		f.status = StatusMotorOn
	default:
		f.status = StatusMotorOn
	}
}

func (f *fdc) startRead() {
	d := f.disk()
	if d == nil {
		f.status = StatusRecordNotFound
		return
	}
	buf, err := d.ReadSector(int(f.track), int(f.sector))
	if err != nil {
		f.status = StatusRecordNotFound
		return
	}
	f.buf, f.pos, f.write = buf, 0, false
	f.status = StatusMotorOn | StatusBusy | StatusDRQ
	f.data = f.buf[0]
}

func (f *fdc) startWrite() {
	d := f.disk()
	if d == nil {
		f.status = StatusRecordNotFound
		return
	}
	if f.drive >= 0 && f.drive < len(f.wprot) && f.wprot[f.drive] {
		f.status = StatusWriteProtect
		return
	}
	if _, err := d.ReadSector(int(f.track), int(f.sector)); err != nil {
		f.status = StatusRecordNotFound
		return
	}
	f.buf = make([]byte, SectorSize)
	f.pos, f.write = 0, true
	f.target.track, f.target.sector = int(f.track), int(f.sector)
	f.status = StatusMotorOn | StatusBusy | StatusDRQ
}

// endTransfer commits a write in progress and clears the transfer state.
func (f *fdc) endTransfer() {
	if f.write && f.buf != nil && f.pos > 0 {
		if d := f.disk(); d != nil {
			_ = d.WriteSector(f.target.track, f.target.sector, f.buf)
		}
	}
	f.buf, f.pos, f.write = nil, 0, false
}

// readData returns the next byte of a read transfer.
func (f *fdc) readData() byte {
	if f.buf == nil || f.write {
		return f.data
	}
	v := f.buf[f.pos]
	f.pos++
	if f.pos >= len(f.buf) {
		f.buf, f.pos = nil, 0
		f.status = StatusMotorOn // transfer complete: BUSY and DRQ clear
	} else {
		f.data = f.buf[f.pos]
	}
	return v
}

// writeData accepts the next byte of a write transfer.
func (f *fdc) writeData(v byte) {
	f.data = v
	if f.buf == nil || !f.write {
		return
	}
	f.buf[f.pos] = v
	f.pos++
	if f.pos >= len(f.buf) {
		if d := f.disk(); d != nil {
			_ = d.WriteSector(f.target.track, f.target.sector, f.buf)
		}
		f.buf, f.pos, f.write = nil, 0, false
		f.status = StatusMotorOn
	}
}
