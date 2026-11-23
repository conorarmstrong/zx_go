package betadisk

// Interface is the Beta Disk hardware: it decodes the I/O ports that front the
// WD1793 and translates the $FF "system register" into drive/side selection and
// controller reset. The five registers respond on the exact low byte, so they
// never collide with the SpecDrum/Covox/Kempston-mouse ports ($DF/$FB).
//
//	$1F  command (write) / status (read)
//	$3F  track register
//	$5F  sector register
//	$7F  data register
//	$FF  system register (write: drive/side/reset/density;
//	     read: bit7 = INTRQ, bit6 = DRQ)
type Interface struct {
	FDC    *FDC
	sysReg byte
}

// NewInterface returns a Beta interface with a fresh, diskless WD1793.
func NewInterface() *Interface {
	return &Interface{FDC: NewFDC()}
}

// SetClock wires the machine T-states clock into the FDC's index-pulse model
// (frameT = one frame length in the same units). Without it the Index status
// bit stays on the legacy head-load mirror.
func (it *Interface) SetClock(clk func() uint64, frameT uint64) { it.FDC.SetClock(clk, frameT) }

// Handles reports whether the given I/O port is one of the Beta registers.
func (it *Interface) Handles(port uint16) bool {
	switch byte(port & 0xFF) {
	case 0x1F, 0x3F, 0x5F, 0x7F, 0xFF:
		return true
	}
	return false
}

// ReadPort returns the value for a Beta register read. Tick first: every port
// access is an event boundary, so a completion armed at command issue (Fuse's
// step/verify event) becomes visible here — the DOS's IN $FF poll is what
// catches the re-armed INTRQ.
func (it *Interface) ReadPort(port uint16) byte {
	it.FDC.Tick()
	var v byte
	switch byte(port & 0xFF) {
	case 0x1F:
		v = it.FDC.ReadStatus()
	case 0x3F:
		v = it.FDC.ReadTrackReg()
	case 0x5F:
		v = it.FDC.ReadSectorReg()
	case 0x7F:
		v = it.FDC.ReadData()
	case 0xFF:
		if it.FDC.Intrq() {
			v |= 0x80
		}
		if it.FDC.Drq() {
			v |= 0x40
		}
	default:
		v = 0xFF
	}
	return v
}

// WritePort applies a Beta register write. Tick first — event boundary, as
// ReadPort.
func (it *Interface) WritePort(port uint16, val byte) {
	it.FDC.Tick()
	switch byte(port & 0xFF) {
	case 0x1F:
		it.FDC.WriteCommand(val)
	case 0x3F:
		it.FDC.WriteTrackReg(val)
	case 0x5F:
		it.FDC.WriteSectorReg(val)
	case 0x7F:
		it.FDC.WriteData(val)
	case 0xFF:
		it.writeSystemRegister(val)
	}
}

// writeSystemRegister decodes the $FF system register (Beta 128), mirroring
// Fuse's beta_sp_write (beta.c:340-353):
//   - bits 0-1: drive select
//   - bit 3:    block-HLT (set normally; controls the HLT trigger)
//   - bit 4:    side select, inverted (set → side 0, clear → side 1)
//   - bit 5:    density (MFM when set)
//
// Fuse performs NO controller reset from this register — the WD1793 is
// master-reset only at machine reset (beta_reset → wd_fdc_master_reset).
func (it *Interface) writeSystemRegister(val byte) {
	it.sysReg = val
	it.FDC.SelectDrive(int(val & 0x03))
	// Fuse also consumes bit 3 (wd_fdc_set_hlt) and bit 5 (dden, MFM when
	// set) here; neither is modelled (TR-DOS is always FM, and the block-
	// HLT path needs no engine state), so only drive/side have effect.
	if val&0x10 != 0 {
		it.FDC.SetSide(0)
	} else {
		it.FDC.SetSide(1)
	}
}
