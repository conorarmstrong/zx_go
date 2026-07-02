package sam

// SAM ASIC light-pen registers, read on port 0xF8. The register is selected by
// bit 8 of the port address (i.e. bit 0 of the high address byte): a port whose
// low 9 bits are 0x0F8 reads LPEN (the horizontal beam position); 0x1F8 reads
// HPEN (the current display line). The boot ROM busy-waits on HPEN to lock to
// the raster, so both must follow the beam derived from the CPU T-state counter
// rather than floating to a constant.

const (
	lpenTXFMST     = 0x02 // LPEN bit 1: transmit-status, preserved across reads
	lpenBorderBCD0 = 0x01 // LPEN bit 0: border / light-pen colour bit
)

// penByte dispatches an IN from port 0xF8 to LPEN or HPEN based on port bit 8.
func (m *Machine) penByte(addr uint16) byte {
	if addr&0x01FF == 0x00F8 {
		return m.updateLPEN()
	}
	return m.updateHPEN()
}

// rasterPos returns the current scan line (0..311) and the T-state offset within
// that line, both relative to the top of the frame.
func (m *Machine) rasterPos() (line, lineCycle int) {
	rel := m.CPU.Tstates() - m.frameStart
	return int(rel / samCyclesPerLine), int(rel % samCyclesPerLine)
}

// screenDisabled reports SOFF blanking, which only takes effect in modes 3/4.
func (m *Machine) screenDisabled() bool {
	return m.border&borderSOFF != 0 && m.Mem.ScreenMode() >= 3
}

// updateHPEN latches and returns the current display line (line-68) while the
// beam is within the active screen, otherwise the screen height (192). When the
// screen is disabled the previously latched value persists.
func (m *Machine) updateHPEN() byte {
	if !m.screenDisabled() {
		line, lineCycle := m.rasterPos()
		if isScreenLine(line) && (line != samTopBorderLines || lineCycle >= 2*samSideBorderCycles) {
			m.hpen = byte(line - samTopBorderLines)
		} else {
			m.hpen = byte(samScreenLines)
		}
	}
	return m.hpen
}

// updateLPEN latches and returns the horizontal beam position in the top six
// bits, preserving the transmit-status bit and carrying the border/colour bit in
// bit 0. The per-pixel light-pen colour bit (which the ASIC samples from the
// live CLUT index) is approximated by the border bit; no SAM software depends on
// it, whereas the position bits are what code actually reads.
func (m *Machine) updateLPEN() byte {
	if !m.screenDisabled() {
		line, lineCycle := m.rasterPos()
		if isScreenLine(line) && lineCycle >= 2*samSideBorderCycles {
			xpos := byte(lineCycle - 2*samSideBorderCycles)
			m.lpen = (xpos & 0xFC) | (m.lpen & lpenTXFMST) | (m.border & lpenBorderBCD0)
		} else {
			m.lpen = (m.lpen & lpenTXFMST) | (m.border & lpenBorderBCD0)
		}
	} else {
		m.lpen = (m.lpen &^ lpenBorderBCD0) | (m.border & lpenBorderBCD0)
	}
	return m.lpen
}

// isScreenLine reports whether a frame-relative scan line falls in the active
// display (lines 68..259).
func isScreenLine(line int) bool {
	return line >= samTopBorderLines && line < samTopBorderLines+samScreenLines
}
