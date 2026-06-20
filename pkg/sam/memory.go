package sam

// Memory is the SAM Coupé memory map: four 16 KB sections (A=$0000, B=$4000,
// C=$8000, D=$C000) paged from a 32 KB ROM (ROM0/ROM1) and up to 512 KB of
// internal RAM via the LMPR/HMPR registers. Section B always follows section A
// by one page, and section D follows section C, so each register controls a
// 32 KB window. (SimCoupe SAMIO.cpp UpdatePaging 231-255.)
//
// Sprint 0 implements the reset map + internal RAM paging + write-protect.
// Sprint 1 adds the register OUT handlers, the VMPR video selection, external
// RAM (LEPR/HEPR/MCNTRL) and 256K masking; Sprint 7 adds ASIC contention.
type Memory struct {
	rom0    [PageSize]byte
	rom1    [PageSize]byte
	ram     [numRAMPages][PageSize]byte
	scratch [PageSize]byte // write sink for ROM / write-protected sections

	// Paging registers (all reset to 0 → ROM0/RAM1/RAM0/RAM1).
	lmpr byte // Low Memory Page Register  (port 0xFA)
	hmpr byte // High Memory Page Register (port 0xFB)
	vmpr byte // Video Memory Page Register (port 0xFC)

	// Resolved per-section page pointers (recomputed by updatePaging).
	readPage  [4]*[PageSize]byte
	writePage [4]*[PageSize]byte
}

const (
	numRAMPages = 32 // 32 × 16 KB = 512 KB internal RAM (the default config)

	pageMask    = 0x1F // LMPR/HMPR page bits 0-4
	lmprROM0Off = 0x20 // LMPR bit 5: 0 = ROM0 paged into section A
	lmprROM1    = 0x40 // LMPR bit 6: 1 = ROM1 paged into section D
	lmprWProt   = 0x80 // LMPR bit 7: write-protect section A
)

// NewMemory builds the SAM memory with the given 16 KB ROM halves and the
// power-on register state (all zero → ROM0 / RAM1 / RAM0 / RAM1).
func NewMemory(rom0, rom1 []byte) *Memory {
	m := &Memory{}
	copy(m.rom0[:], rom0)
	copy(m.rom1[:], rom1)
	m.updatePaging()
	return m
}

// updatePaging resolves the four section read/write pointers from LMPR/HMPR.
func (m *Memory) updatePaging() {
	// Section A ($0000): ROM0 unless LMPR bit 5 (ROM0_OFF) is set.
	if m.lmpr&lmprROM0Off == 0 {
		m.readPage[0] = &m.rom0
		m.writePage[0] = &m.scratch
	} else {
		p := &m.ram[m.lmpr&pageMask]
		m.readPage[0], m.writePage[0] = p, p
	}
	if m.lmpr&lmprWProt != 0 {
		m.writePage[0] = &m.scratch // write-protected
	}

	// Section B ($4000): always RAM, one page after section A.
	pb := &m.ram[(m.lmpr+1)&pageMask]
	m.readPage[1], m.writePage[1] = pb, pb

	// Section C ($8000): RAM page HMPR. (External RAM via MCNTRL: Sprint 1.)
	pc := &m.ram[m.hmpr&pageMask]
	m.readPage[2], m.writePage[2] = pc, pc

	// Section D ($C000): ROM1 if LMPR bit 6 set, else RAM one page after C.
	if m.lmpr&lmprROM1 != 0 {
		m.readPage[3] = &m.rom1
		m.writePage[3] = &m.scratch
	} else {
		pd := &m.ram[(m.hmpr+1)&pageMask]
		m.readPage[3], m.writePage[3] = pd, pd
	}
}

// Read returns the byte at addr through the current page map.
func (m *Memory) Read(addr uint16) byte {
	return m.readPage[addr>>14][addr&(PageSize-1)]
}

// Write stores val at addr; ROM and write-protected sections sink to scratch.
func (m *Memory) Write(addr uint16, val byte) {
	m.writePage[addr>>14][addr&(PageSize-1)] = val
}

// ContendPort is the per-I/O ASIC contention hook. No-op until Sprint 7.
func (m *Memory) ContendPort(port uint16) {}

// SetLMPR / SetHMPR / SetVMPR update a paging register and re-resolve the map.
// Sprint 2 calls these from the I/O port dispatch.
func (m *Memory) SetLMPR(v byte) { m.lmpr = v; m.updatePaging() }
func (m *Memory) SetHMPR(v byte) { m.hmpr = v; m.updatePaging() }
func (m *Memory) SetVMPR(v byte) { m.vmpr = v }

// LMPR / HMPR / VMPR read back the stored register values.
func (m *Memory) LMPR() byte { return m.lmpr }
func (m *Memory) HMPR() byte { return m.hmpr }
func (m *Memory) VMPR() byte { return m.vmpr }
