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
//
// Data transfer is DRQ-driven: the Opus wires DRQ to the Z80's NMI, so each
// byte of a Type II/III command raises an interrupt and the ROM's handler
// moves exactly one byte through the data register. There is no DMA and no
// polling loop over DRQ in the ROM.
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

	// nmi is pulsed on each DRQ rising edge.
	nmi func()

	// DRQ timing. A real WD1770 reading double-density at 250 kbit/s hands
	// over a byte roughly every 32 microseconds, and that spacing is
	// load-bearing rather than cosmetic: the ROM's handler needs about 83
	// T-states to run, so a DRQ asserted the instant the previous byte was
	// taken would re-enter NMI_READ at $188C, halfway through itself, instead
	// of after its RETN. The transfer would then never advance.
	now       uint64 // last T-state seen, fed by tickTo
	remaining int    // T-states still to wait for the next byte
	waiting   bool   // a byte is due but not yet handed over
	started   bool   // now holds a real reading
}

// BytePeriodTStates is the gap between successive bytes of a transfer:
// 32 microseconds at the Spectrum's 3.5 MHz.
const BytePeriodTStates = 112

// tickTo advances the controller's clock and hands over the next byte once a
// byte-period has passed.
//
// The counter it is given is rebased at every frame boundary rather than
// running absolutely, so this counts DOWN from a delta rather than comparing
// against a deadline. A deadline would be missed for a whole counter cycle
// each time the frame rebased, stalling the transfer mid-sector with BUSY
// still asserted.
func (f *fdc) tickTo(now uint64) {
	delta := uint64(0)
	switch {
	case !f.started:
		f.started = true
	case now >= f.now:
		delta = now - f.now
	default:
		// The counter was rebased. Everything up to the new reading is
		// elapsed time; the part before the rebase is lost, which at worst
		// delays one byte by less than a frame.
		delta = now
	}
	f.now = now
	if !f.waiting {
		return
	}
	if uint64(f.remaining) > delta {
		f.remaining -= int(delta)
		return
	}
	f.remaining, f.waiting = 0, false
	f.raiseDRQ()
}

// scheduleDRQ arms the next byte one byte-period from now.
func (f *fdc) scheduleDRQ() {
	f.waiting, f.remaining = true, BytePeriodTStates
}

// raiseDRQ asserts DRQ and pulses the NMI line, which is what actually
// fetches the next byte out of the guest.
func (f *fdc) raiseDRQ() {
	f.status |= StatusDRQ
	if f.nmi != nil {
		f.nmi()
	}
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
	f.waiting = false
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
	case cmd&0xF0 == 0xD0: // FORCE INTERRUPT — the ROM's BREAK path uses $D0
		f.buf, f.pos, f.write, f.waiting = nil, 0, false, false
		f.status = StatusMotorOn
	case cmd&0xF0 == 0xF0: // WRITE TRACK — formatting, not implemented
		// Refuse rather than report success. The ROM checks bits 6 and 2 after
		// a format ("AND +44"), so write-protect is the signal that reaches
		// the user as an error. Completing silently would have the ROM believe
		// it had laid down a track and write a catalogue over an image that
		// still holds the old data.
		f.status = StatusMotorOn | StatusWriteProtect
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
	f.status = StatusMotorOn | StatusBusy
	f.data = f.buf[0]
	f.scheduleDRQ()
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
	f.status = StatusMotorOn | StatusBusy
	f.scheduleDRQ()
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
		// Transfer complete: BUSY and DRQ drop, which is what lets the ROM's
		// WAIT_I/O loop ("LD A,(2800) / BIT 0,A / RET Z") finish.
		f.status = StatusMotorOn
	} else {
		f.data = f.buf[f.pos]
		f.status &^= StatusDRQ
		f.scheduleDRQ()
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
	} else {
		f.status &^= StatusDRQ
		f.scheduleDRQ()
	}
}
