package sam

// SAM ASIC memory/IO contention. The ASIC holds the bus when the CPU touches
// internal memory during the active display: accesses are rounded up to an
// 8-T boundary in the screen window and a 4-T boundary elsewhere. Three
// per-T-state delay tables are precomputed (mode 1 contends extra alternating
// bands; modes 2/3/4 only in the main screen; 4T is the screen-disabled/border
// baseline).

const (
	samContentionOffset = 4 // CPU_CYCLES_SCREEN_CONTENTION_OFFSET
	samSideBorderCycles = 64
	samScreenLines      = 192
)

var (
	contentionMode1   []byte
	contentionMode234 []byte
	contention4T      []byte
)

func init() {
	n := CyclesPerFrame + 64
	contentionMode1 = make([]byte, n)
	contentionMode234 = make([]byte, n)
	contention4T = make([]byte, n)
	for t := 0; t < n; t++ {
		line := t / samCyclesPerLine
		lineCycle := (t + samContentionOffset) % samCyclesPerLine
		mainScreen := line >= samTopBorderLines && line < samTopBorderLines+samScreenLines &&
			lineCycle >= 2*samSideBorderCycles
		mode1Band := lineCycle&0x40 == 0
		round := func(mask int) byte { return byte(mask - ((t + 2) & mask)) }
		m1mask, m234mask := 3, 3
		if mainScreen || mode1Band {
			m1mask = 7
		}
		if mainScreen {
			m234mask = 7
		}
		contentionMode1[t] = round(m1mask)
		contentionMode234[t] = round(m234mask)
		contention4T[t] = round(3)
	}
}

// SetTStatePtr receives the CPU's frame-relative T-state counter (the z80
// contentionWirer hook). Wiring it also makes z80.New register ContendMemory.
func (m *Memory) SetTStatePtr(p *uint64) { m.tstatePtr = p }

// SetContentionEnabled toggles ASIC contention (false = the ZX_GO_SAM_NO_CONTENTION
// A/B mode: no memory or I/O wait states at all).
func (m *Memory) SetContentionEnabled(on bool) { m.contentionEnabled = on }

// SetScreenOff records BORDER bit 7 (SOFF); in modes 3/4 it disables the screen,
// which removes screen contention (falls back to the 4T table).
func (m *Memory) SetScreenOff(off bool) {
	m.screenOff = off
	m.updateContention()
}

// updateContention selects the active delay table for the current screen state.
func (m *Memory) updateContention() {
	switch {
	case m.screenOff && m.ScreenMode() >= 3: // SOFF blanks modes 3/4
		m.activeContention = contention4T
	case m.ScreenMode() == 1:
		m.activeContention = contentionMode1
	default:
		m.activeContention = contentionMode234
	}
}

// delayAt returns the contention delay for the current frame T-state.
func (m *Memory) delayAt() uint64 {
	t := int(*m.tstatePtr)
	if t < 0 || t >= len(m.activeContention) {
		return 0
	}
	return uint64(m.activeContention[t])
}

// ContendMemory adds the ASIC bus-hold delay when the CPU accesses contended
// (internal-RAM) memory. ROM, external RAM and scratch are uncontended.
func (m *Memory) ContendMemory(addr uint16) {
	if !m.contentionEnabled || m.tstatePtr == nil {
		return
	}
	if m.sectionPage[addr>>14] >= 0 { // internal RAM page → contended
		*m.tstatePtr += m.delayAt()
	}
}

// ContendPort adds I/O contention for ASIC ports (low byte ≥ 0xF8), rounded up
// to an 8-T boundary.
func (m *Memory) ContendPort(port uint16) {
	if !m.contentionEnabled || m.tstatePtr == nil {
		return
	}
	if byte(port&0xFF) >= 0xF8 {
		t := *m.tstatePtr
		*m.tstatePtr += uint64(7 - ((t + 2) & 7))
	}
}
