package sam

// SAM I/O port map (low-byte decode; the high address byte sub-selects keyboard
// rows / CLUT index).
const (
	portKempston = 0x1F // IN: Kempston joystick
	portLEPR     = 0x80 // OUT: low external page register
	portHEPR     = 0x81 // OUT: high external page register
	portClock    = 0xEF // IN/OUT: SAMBUS/DALLAS clock
	portCLUT     = 0xF8 // OUT: CLUT palette (index = high byte & 0x0F); IN: light pen
	portStatus   = 0xF9 // IN: STATUS (interrupts + kbd hi bits); OUT: LINE interrupt
	portLMPR     = 0xFA // IN/OUT: low memory page register
	portHMPR     = 0xFB // IN/OUT: high memory page register
	portVMPR     = 0xFC // IN/OUT: video memory page register
	portMIDI     = 0xFD // IN/OUT: MIDI
	portKeyboard = 0xFE // IN: keyboard + EAR/SPEN/SOFF; OUT: BORDER
	portSAA      = 0xFF // OUT: SAA1099 sound; IN: ASIC attribute byte
)

// BORDER (OUT 0xFE) bit fields.
const (
	borderMIC  = 0x08
	borderBEEP = 0x10
	borderSOFF = 0x80
)

// STATUS (IN 0xF9) interrupt bits, active-low (0 = pending).
const (
	statusLine    = 0x01
	statusFrame   = 0x08
	statusIntMask = 0x1F
	statusKeyMask = 0xE0

	keyboardEAR = 0x40 // tape input, idle high
)

// ReadPort implements the z80.ULA IN side: low-byte port dispatch, with the
// high address byte selecting keyboard rows / sub-registers.
func (m *Machine) ReadPort(addr uint16) (byte, bool) {
	low := byte(addr & 0xFF)
	high := byte(addr >> 8)
	switch low {
	case portKeyboard: // keyboard columns 0-4 + EAR + SOFF
		scan := m.Kbd.Scan(high)
		return (scan & 0x1F) | keyboardEAR | (m.border & borderSOFF), true
	case portStatus: // keyboard columns 5-7 + interrupt status (active-low)
		scan := m.Kbd.Scan(high)
		return (scan & statusKeyMask) | (m.statusByte() & statusIntMask), true
	case portLMPR:
		return m.Mem.LMPR(), true
	case portHMPR:
		return m.Mem.HMPR(), true
	case portVMPR:
		return m.Mem.VMPR(), true
	case portCLUT: // ASIC light-pen registers: LPEN (0x0F8) / HPEN (0x1F8)
		return m.penByte(addr), true
	case portKempston:
		return 0x00, true // no joystick attached (active-high, idle 0)
	default:
		if fdc, reg := m.floppy(low); fdc != nil {
			fdc.SetSide(int(low>>2) & 1)
			switch reg {
			case 0:
				return fdc.ReadStatus(), true
			case 1:
				return fdc.ReadTrack(), true
			case 2:
				return fdc.ReadSector(), true
			default:
				return fdc.ReadData(), true
			}
		}
		// Unhandled ASIC/peripheral reads float high (ATTR 0xFF, MIDI, clock).
		return 0xFF, true
	}
}

// floppy maps a low-byte port to its WD1772 (drive 1 = 0xE0-E7, drive 2 =
// 0xF0-F7) and register index (port & 3), or nil if not a floppy port.
func (m *Machine) floppy(low byte) (*WD1772, int) {
	switch low & 0xF8 {
	case 0xE0:
		return m.FDC[0], int(low & 3)
	case 0xF0:
		return m.FDC[1], int(low & 3)
	}
	return nil, 0
}

// WritePort implements the z80.ULA OUT side.
func (m *Machine) WritePort(addr uint16, val byte) {
	low := byte(addr & 0xFF)
	high := byte(addr >> 8)
	switch low {
	case portKeyboard: // BORDER (colour + MIC + BEEP + SOFF)
		m.flushRaster() // border/SOFF affect display: flush at the old value first
		m.border = val
		m.Mem.SetScreenOff(val&borderSOFF != 0) // SOFF changes screen contention
	case portLMPR:
		m.Mem.SetLMPR(val)
	case portHMPR:
		m.flushRaster() // HMPR carries the mode-3 colour bits
		m.Mem.SetHMPR(val)
	case portVMPR:
		m.flushRaster() // mode/page change
		m.Mem.SetVMPR(val)
	case portLEPR:
		m.Mem.SetLEPR(val)
	case portHEPR:
		m.Mem.SetHEPR(val)
	case portCLUT: // palette register: index in high byte, 7-bit colour in val
		m.flushRaster() // palette change
		m.clut[high&0x0F] = val & 0x7F
	case portStatus: // LINE interrupt target line
		m.setLineInterrupt(val)
	case portSAA: // SAA1099: high byte 0x01 = address latch, else data
		if addr&0x0100 != 0 {
			m.SAA.WriteAddress(val)
		} else {
			m.SAA.WriteData(val)
		}
	default:
		if fdc, reg := m.floppy(low); fdc != nil {
			fdc.SetSide(int(low>>2) & 1)
			switch reg {
			case 0:
				fdc.WriteCommand(val)
			case 1:
				fdc.WriteTrack(val)
			case 2:
				fdc.WriteSector(val)
			default:
				fdc.WriteData(val)
			}
			return
		}
		// MIDI, clock and SD/IDE ports are not modelled; writes are ignored.
	}
}

// statusByte computes the active-low interrupt bits (1 = inactive) from the
// current frame-relative T-state: the frame INT is pending for samIntActiveCycles
// from samFrameIntTstate; the line INT (when enabled) from its scan-line tstate.
func (m *Machine) statusByte() byte {
	s := byte(statusIntMask) // all interrupts inactive
	rel := m.CPU.Tstates() - m.frameStart
	if rel >= samFrameIntTstate && rel < samFrameIntTstate+samIntActiveCycles {
		s &^= statusFrame
	}
	if m.line < 192 {
		lt := uint64((int(m.line) + 68) * 384)
		if rel >= lt && rel < lt+samIntActiveCycles {
			s &^= statusLine
		}
	}
	return s
}

// setLineInterrupt programs the line-interrupt target (OUT 0xF9). Lines 0-191
// fire the maskable INT a second time at (line+68)·384; >=192 disables it. The
// shared Z80 line-INT hook raises the same IRQ latch the frame INT uses.
func (m *Machine) setLineInterrupt(line byte) {
	m.line = line
	if line < 192 {
		m.CPU.LineIntOffsetTstates = uint64((int(line) + 68) * 384)
	} else {
		m.CPU.LineIntOffsetTstates = 0
	}
}
