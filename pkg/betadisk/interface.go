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

// Handles reports whether the given I/O port is one of the Beta registers.
func (it *Interface) Handles(port uint16) bool {
	switch byte(port & 0xFF) {
	case 0x1F, 0x3F, 0x5F, 0x7F, 0xFF:
		return true
	}
	return false
}

// ReadPort returns the value for a Beta register read.
func (it *Interface) ReadPort(port uint16) byte {
	switch byte(port & 0xFF) {
	case 0x1F:
		return it.FDC.ReadStatus()
	case 0x3F:
		return it.FDC.ReadTrackReg()
	case 0x5F:
		return it.FDC.ReadSectorReg()
	case 0x7F:
		return it.FDC.ReadData()
	case 0xFF:
		var b byte
		if it.FDC.Intrq() {
			b |= 0x80
		}
		if it.FDC.Drq() {
			b |= 0x40
		}
		return b
	}
	return 0xFF
}

// WritePort applies a Beta register write.
func (it *Interface) WritePort(port uint16, val byte) {
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

// writeSystemRegister decodes the $FF system register (Beta 128):
//   - bits 0-1: drive select
//   - bit 2:    reset (active low — clear bit resets the controller)
//   - bit 4:    side select, inverted (set → side 0, clear → side 1)
//   - bit 5:    density (MFM when set) — not modelled (TR-DOS is always MFM)
func (it *Interface) writeSystemRegister(val byte) {
	it.sysReg = val
	it.FDC.SelectDrive(int(val & 0x03))
	if val&0x10 != 0 {
		it.FDC.SetSide(0)
	} else {
		it.FDC.SetSide(1)
	}
	if val&0x04 == 0 {
		it.FDC.Reset()
	}
}
