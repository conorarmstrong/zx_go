package sam

// Memory is the SAM Coupé memory map. Four 16 KB sections — A=$0000, B=$4000,
// C=$8000, D=$C000 — are paged from a 32 KB ROM (ROM0/ROM1), up to 512 KB
// internal RAM and up to 4 MB external RAM, via five registers:
//
//	LMPR (0xFA): page (sec A; B=page+1), bit5 ROM0-off, bit6 ROM1→D, bit7 WPROT-A
//	HMPR (0xFB): page (sec C; D=page+1), bit7 MCNTRL → external RAM in C/D
//	VMPR (0xFC): displayed video page (0-4) + screen mode (bits 5-6); NOT a CPU map
//	LEPR (0x80): 8-bit external page for section C (when MCNTRL)
//	HEPR (0x81): 8-bit external page for section D (when MCNTRL)
//
// Section B always follows A by one page and D follows C, so LMPR/HMPR each
// control a 32 KB window. ROM and write-protected/absent pages sink writes to a
// scratch page and read from a separate zero scratch page.
type Memory struct {
	rom0 [PageSize]byte
	rom1 [PageSize]byte

	ram    [][PageSize]byte // internal RAM: 16 pages (256K) or 32 (512K)
	extRAM [][PageSize]byte // external RAM: 64 pages per MB, up to 256 (4 MB)

	scratchRead  [PageSize]byte // reads from absent/scratch pages (zero)
	scratchWrite [PageSize]byte // sink for ROM / write-protected / absent writes

	// Paging registers (all reset to 0 → ROM0/RAM1/RAM0/RAM1).
	lmpr byte
	hmpr byte
	vmpr byte
	lepr byte
	hepr byte

	// Resolved per-section pointers + the internal RAM page each section maps to
	// (-1 = ROM/external/scratch), used for video-write detection.
	readPage    [4]*[PageSize]byte
	writePage   [4]*[PageSize]byte
	sectionPage [4]int

	// videoSection[s] is true when section s currently overlays the displayed
	// video page (or, in modes 3/4, the second page of the 24 KB bitmap).
	videoSection [4]bool

	// onVideoWrite, when set, is invoked whenever the CPU writes to displayed
	// video memory — the hook the renderer uses for line-accurate updates.
	onVideoWrite func()

	// ASIC contention. tstatePtr is the CPU's frame-relative T-state
	// counter (shared via SetTStatePtr); activeContention is the per-T-state
	// delay table for the current screen mode; contentionEnabled gates it all
	// (ZX_GO_SAM_NO_CONTENTION opt-out); screenOff mirrors BORDER bit 7.
	tstatePtr         *uint64
	activeContention  []byte
	contentionEnabled bool
	screenOff         bool
}

const (
	numRAMPages = 32 // 32 × 16 KB = 512 KB internal RAM (default config)

	pageMask    = 0x1F // LMPR/HMPR page bits 0-4
	lmprROM0Off = 0x20 // LMPR bit 5: 0 = ROM0 paged into section A
	lmprROM1    = 0x40 // LMPR bit 6: 1 = ROM1 paged into section D
	lmprWProt   = 0x80 // LMPR bit 7: write-protect section A
	hmprMCNTRL  = 0x80 // HMPR bit 7: external RAM in sections C/D

	vmprPageMask = 0x1F // VMPR video-page bits 0-4
	vmprModeMask = 0x60 // VMPR screen-mode bits 5-6
	vmprMDE1     = 0x40 // VMPR bit 6 set ⇒ mode 3 or 4 (24 KB bitmap, 2 pages)
	vmprRXMIDI   = 0x80 // VMPR bit 7: MIDI-in status, OR'd into reads, dropped on write

	extPagesPerMB = 64 // 1 MB / 16 KB
)

// NewMemory builds the SAM memory with 512 KB internal RAM and no external RAM
// (the default config) from the two 16 KB ROM halves.
func NewMemory(rom0, rom1 []byte) *Memory {
	return NewMemoryWithRAM(rom0, rom1, 512, 0)
}

// NewMemoryWithRAM builds the SAM memory with internalKB (256 or 512) of
// internal RAM and externalMB (0..4) of external RAM, at the power-on register
// state (all zero → ROM0 / RAM1 / RAM0 / RAM1).
func NewMemoryWithRAM(rom0, rom1 []byte, internalKB, externalMB int) *Memory {
	m := &Memory{}
	copy(m.rom0[:], rom0)
	copy(m.rom1[:], rom1)
	intPages := internalKB / 16
	if intPages > numRAMPages {
		intPages = numRAMPages
	}
	m.ram = make([][PageSize]byte, intPages)
	if externalMB > 4 {
		externalMB = 4
	}
	if externalMB > 0 {
		m.extRAM = make([][PageSize]byte, externalMB*extPagesPerMB)
	}
	m.contentionEnabled = true
	m.updatePaging()
	m.updateContention()
	return m
}

// ramPage returns the read/write pointers for internal RAM page p (masked to 5
// bits); absent pages (e.g. 16-31 on a 256K machine) resolve to scratch.
func (m *Memory) ramPage(p byte) (rd, wr *[PageSize]byte, idx int) {
	p &= pageMask
	if int(p) < len(m.ram) {
		return &m.ram[p], &m.ram[p], int(p)
	}
	return &m.scratchRead, &m.scratchWrite, -1
}

// extPage returns the read/write pointers for external RAM page p (full 8 bits);
// pages beyond the installed expansion resolve to scratch.
func (m *Memory) extPage(p byte) (rd, wr *[PageSize]byte) {
	if int(p) < len(m.extRAM) {
		return &m.extRAM[p], &m.extRAM[p]
	}
	return &m.scratchRead, &m.scratchWrite
}

// updatePaging resolves the four section read/write pointers from the registers.
func (m *Memory) updatePaging() {
	// Section A ($0000): ROM0 unless LMPR bit 5 (ROM0_OFF) set.
	if m.lmpr&lmprROM0Off == 0 {
		m.readPage[0], m.writePage[0], m.sectionPage[0] = &m.rom0, &m.scratchWrite, -1
	} else {
		rd, wr, idx := m.ramPage(m.lmpr)
		m.readPage[0], m.writePage[0], m.sectionPage[0] = rd, wr, idx
	}
	if m.lmpr&lmprWProt != 0 {
		m.writePage[0] = &m.scratchWrite // section A write-protected
	}

	// Section B ($4000): always RAM, one page after section A.
	rd, wr, idx := m.ramPage(m.lmpr + 1)
	m.readPage[1], m.writePage[1], m.sectionPage[1] = rd, wr, idx

	// Section C ($8000): external RAM (LEPR) when MCNTRL, else RAM page HMPR.
	if m.hmpr&hmprMCNTRL != 0 {
		rd, wr := m.extPage(m.lepr)
		m.readPage[2], m.writePage[2], m.sectionPage[2] = rd, wr, -1
	} else {
		rd, wr, idx := m.ramPage(m.hmpr)
		m.readPage[2], m.writePage[2], m.sectionPage[2] = rd, wr, idx
	}

	// Section D ($C000): external RAM (HEPR) when MCNTRL; else ROM1 if LMPR
	// bit 6; else RAM one page after C.
	switch {
	case m.hmpr&hmprMCNTRL != 0:
		rd, wr := m.extPage(m.hepr)
		m.readPage[3], m.writePage[3], m.sectionPage[3] = rd, wr, -1
	case m.lmpr&lmprROM1 != 0:
		m.readPage[3], m.writePage[3], m.sectionPage[3] = &m.rom1, &m.scratchWrite, -1
	default:
		rd, wr, idx := m.ramPage(m.hmpr + 1)
		m.readPage[3], m.writePage[3], m.sectionPage[3] = rd, wr, idx
	}

	m.recomputeVideoSections()
}

// recomputeVideoSections flags which sections currently overlay displayed video
// memory: the VMPR page, plus VMPR-page+1 in modes 3/4 (24 KB two-page bitmap).
func (m *Memory) recomputeVideoSections() {
	disp := int(m.VisibleScreenPage())
	twoPage := m.vmpr&vmprMDE1 != 0
	for s := 0; s < 4; s++ {
		p := m.sectionPage[s]
		m.videoSection[s] = p >= 0 && (p == disp || (twoPage && p == (disp+1)&pageMask))
	}
}

// Read returns the byte at addr through the current page map.
func (m *Memory) Read(addr uint16) byte {
	return m.readPage[addr>>14][addr&(PageSize-1)]
}

// Write stores val at addr; ROM and write-protected/absent sections sink to
// scratch. A write to displayed video memory fires onVideoWrite (if set).
func (m *Memory) Write(addr uint16, val byte) {
	sec := addr >> 14
	m.writePage[sec][addr&(PageSize-1)] = val
	if m.onVideoWrite != nil && m.videoSection[sec] {
		m.onVideoWrite()
	}
}

// Paging-register writes (called by the I/O port dispatch). LMPR, HMPR, LEPR
// and HEPR change the CPU map; VMPR only selects the displayed page.
func (m *Memory) SetLMPR(v byte) { m.lmpr = v; m.updatePaging() }
func (m *Memory) SetHMPR(v byte) { m.hmpr = v; m.updatePaging() }
func (m *Memory) SetLEPR(v byte) { m.lepr = v; m.updatePaging() }
func (m *Memory) SetHEPR(v byte) { m.hepr = v; m.updatePaging() }

// SetVMPR stores the video register (only bits 0-6; RXMIDI is read-only) and
// re-evaluates which sections are displayed. It does not change the CPU map.
func (m *Memory) SetVMPR(v byte) {
	m.vmpr = v & (vmprModeMask | vmprPageMask)
	m.recomputeVideoSections()
	m.updateContention() // screen mode selects the contention table
}

// Reset returns the paging registers to their power-on state (ROM0 in section A,
// internal RAM in B/C/D, no external paging, screen enabled), leaving installed
// RAM/ROM contents untouched. The boot ROM re-initialises RAM itself.
func (m *Memory) Reset() {
	m.lmpr, m.hmpr, m.vmpr, m.lepr, m.hepr = 0, 0, 0, 0, 0
	m.screenOff = false
	m.updatePaging()
	m.updateContention()
}

// Register read-backs. VMPR reads OR in the RXMIDI status bit; the others read
// back the stored value.
func (m *Memory) LMPR() byte { return m.lmpr }
func (m *Memory) HMPR() byte { return m.hmpr }
func (m *Memory) VMPR() byte { return vmprRXMIDI | m.vmpr }
func (m *Memory) LEPR() byte { return m.lepr }
func (m *Memory) HEPR() byte { return m.hepr }

// ScreenMode returns the active SAM screen mode 1-4 (VMPR bits 5-6 + 1).
func (m *Memory) ScreenMode() int { return int((m.vmpr&vmprModeMask)>>5) + 1 }

// VisibleScreenPage returns the physical RAM page holding the displayed image.
// Modes 3/4 use a 24 KB bitmap over two pages, so the page is forced even.
func (m *Memory) VisibleScreenPage() byte {
	if m.vmpr&vmprMDE1 != 0 {
		return m.vmpr & (vmprPageMask &^ 1)
	}
	return m.vmpr & vmprPageMask
}

// VideoMemByte reads displayed video memory by linear offset (0-based from the
// start of the displayed page), spanning into the next page for modes 3/4. The
// renderer uses this so it doesn't care how the bitmap is paged.
func (m *Memory) VideoMemByte(off int) byte {
	page := int(m.VisibleScreenPage()) + off/PageSize
	rd, _, _ := m.ramPage(byte(page))
	return rd[off%PageSize]
}

// SetVideoWriteHook installs a callback fired on every CPU write to displayed
// video memory. Pass nil to detach.
func (m *Memory) SetVideoWriteHook(fn func()) { m.onVideoWrite = fn }
