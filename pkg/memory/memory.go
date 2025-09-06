package memory

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
)

const (
	// PageSize is the size of a memory page in bytes (16KB).
	PageSize = 0x4000
	// RAMSize48K is the total RAM for a 48K Spectrum (48KB).
	RAMSize48K = 0xC000
	// RAMSize128K is the total RAM for a 128K Spectrum (128KB).
	RAMSize128K = 0x20000
)

// Memory represents the computer's memory, including RAM and ROM.
type Memory struct {
	// RAM banks for 128K model (8 pages of 16KB)
	ram [8][]byte
	// ROM banks (4 pages of 16KB for various models)
	rom [4][]byte

	// memoryPageReadMap maps the 4 memory slots (0-3) to the actual memory page index.
	// Slot 0: 0x0000-0x3FFF
	// Slot 1: 0x4000-0x7FFF
	// Slot 2: 0x8000-0xBFFF
	// Slot 3: 0xC000-0xFFFF
	memoryPageReadMap [4]int
	// memoryPageWriteMap is similar to memoryPageReadMap but for write operations.
	memoryPageWriteMap [4]int

	// PagingEnabled indicates if memory paging is allowed.
	PagingEnabled bool
	// ScreenPage is the RAM page currently used for the display (5 or 7).
	ScreenPage int
}

// New creates a new Memory instance for a given machine model.
func New(romPath string, is128k bool) (*Memory, error) {
	m := &Memory{}

	// Initialize RAM pages
	for i := 0; i < 8; i++ {
		m.ram[i] = make([]byte, PageSize)
	}

	// Initialize ROM pages
	for i := 0; i < 4; i++ {
		m.rom[i] = make([]byte, PageSize)
	}

	// Load ROMs
	if err := m.loadROMs(romPath); err != nil {
		return nil, err
	}

	if is128k {
		m.SetModel128K()
	} else {
		m.SetModel48K()
	}

	return m, nil
}

func (m *Memory) loadROMs(romPath string) error {
	var err error
	m.rom[0], err = ioutil.ReadFile(filepath.Join(romPath, "128-0.rom"))
	if err != nil {
		return fmt.Errorf("failed to load 128-0.rom: %w", err)
	}
	m.rom[1], err = ioutil.ReadFile(filepath.Join(romPath, "128-1.rom"))
	if err != nil {
		return fmt.Errorf("failed to load 128-1.rom: %w", err)
	}
	m.rom[2], err = ioutil.ReadFile(filepath.Join(romPath, "48.rom"))
	if err != nil {
		return fmt.Errorf("failed to load 48.rom: %w", err)
	}
	// Placeholder for Pentagon ROM if needed later
	// m.rom[3], err = ioutil.ReadFile(filepath.Join(romPath, "pentagon-0.rom"))
	return nil
}

// SetModel48K configures memory for the 48K Spectrum.
func (m *Memory) SetModel48K() {
	// ROM is in the first 16K
	m.memoryPageReadMap = [4]int{18, 5, 2, 0} // ROM 2 (48.rom), RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM is not writable
	m.PagingEnabled = false
	m.ScreenPage = 5
}

// SetModel128K configures memory for the 128K Spectrum.
func (m *Memory) SetModel128K() {
	// Default to ROM 0 in the first 16K
	m.memoryPageReadMap = [4]int{16, 5, 2, 0} // ROM 0 (128-0.rom), RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM is not writable
	m.PagingEnabled = true
	m.ScreenPage = 5
}

// Read returns the byte at the given address.
func (m *Memory) Read(addr uint16) byte {
	pageIndex := m.memoryPageReadMap[addr>>14]
	offset := addr & (PageSize - 1)

	if pageIndex >= 16 { // ROM
		return m.rom[pageIndex-16][offset]
	}
	// RAM
	return m.ram[pageIndex][offset]
}

// Write sets the byte at the given address.
func (m *Memory) Write(addr uint16, val byte) {
	pageIndex := m.memoryPageWriteMap[addr>>14]
	if pageIndex == -1 {
		// Attempt to write to ROM, ignore.
		return
	}
	offset := addr & (PageSize - 1)
	m.ram[pageIndex][offset] = val
}

// GetPage returns a direct pointer to a RAM page.
func (m *Memory) GetPage(pageIndex int) []byte {
	return m.ram[pageIndex]
}

// PageMemory handles the 128K memory paging mechanism.
func (m *Memory) PageMemory(val byte) {
	if !m.PagingEnabled {
		return
	}

	// Bits 0-2: RAM page to map into 0xC000-0xFFFF
	ramPage := int(val & 0x07)
	m.memoryPageReadMap[3] = ramPage
	m.memoryPageWriteMap[3] = ramPage

	// Bit 3: Screen page (0 for page 5, 1 for page 7)
	if (val & 0x08) != 0 {
		m.ScreenPage = 7
	} else {
		m.ScreenPage = 5
	}

	// Bit 4: ROM select (0 for 128-0.rom, 1 for 128-1.rom)
	if (val & 0x10) != 0 {
		m.memoryPageReadMap[0] = 17 // ROM 1
	} else {
		m.memoryPageReadMap[0] = 16 // ROM 0
	}

	// Bit 5: Paging disable
	if (val & 0x20) != 0 {
		m.PagingEnabled = false
	}
}