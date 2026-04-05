package memory

import (
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/roms"
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
	// Plus3 special paging mode
	specialPaging bool
	port1FFD      byte

	// ROM manager for handling multiple ROM types
	romManager *roms.ROMManager
	// Current Spectrum model
	currentModel roms.SpectrumModel

	// Contention: T-state counter reference set by CPU each frame
	ContentionEnabled bool
	TStates           *uint64 // Pointer to CPU's T-state counter
}

// Contention delay pattern (repeats every 8 T-states in contended region)
var contentionPattern = [8]uint64{6, 5, 4, 3, 2, 1, 0, 0}

// ContendMemory adds contention delay if the address is in contended memory.
// Contended region is 0x4000-0x7FFF (bank 5) on 48K/128K.
func (m *Memory) ContendMemory(addr uint16) {
	if !m.ContentionEnabled || m.TStates == nil {
		return
	}
	// Only contend addresses in the 0x4000-0x7FFF range (screen RAM)
	if addr >= 0x4000 && addr < 0x8000 {
		// Contention applies during the active display (T-states 14335-57343 for 48K)
		tstate := *m.TStates
		if tstate >= 14335 && tstate < 57344 {
			line := (tstate - 14335) / 228
			if line < 192 {
				pos := (tstate - 14335) % 228
				if pos < 128 { // Only during the pixel-drawing portion
					delay := contentionPattern[pos%8]
					*m.TStates += delay
				}
			}
		}
	}
}

// New creates a new Memory instance for a given machine model.
func New(romPath string, model roms.SpectrumModel) (*Memory, error) {
	m := &Memory{
		romManager:   roms.NewROMManager(romPath),
		currentModel: model,
	}

	// Initialize RAM pages
	for i := 0; i < 8; i++ {
		m.ram[i] = make([]byte, PageSize)
	}

	// Initialize ROM pages
	for i := 0; i < 4; i++ {
		m.rom[i] = make([]byte, PageSize)
	}

	// Load ROMs for the specified model
	if err := m.romManager.LoadROMsForModel(model); err != nil {
		return nil, fmt.Errorf("failed to load ROMs for model %s: %w", roms.GetModelName(model), err)
	}

	// Load peripheral ROMs (optional, errors are logged as warnings)
	_ = m.romManager.LoadPeripheralROMs()

	// Set up memory mapping for the model
	if err := m.setupModel(model); err != nil {
		return nil, err
	}

	return m, nil
}

// NewLegacy creates a Memory instance with the legacy interface for compatibility
func NewLegacy(romPath string, is128k bool) (*Memory, error) {
	model := roms.Model48K
	if is128k {
		model = roms.Model128K
	}
	return New(romPath, model)
}

// setupModel configures memory layout and loads ROMs for the specified model
func (m *Memory) setupModel(model roms.SpectrumModel) error {
	switch model {
	case roms.Model48K:
		return m.setup48K()
	case roms.Model128K:
		return m.setup128K()
	case roms.ModelPlus2:
		return m.setupPlus2()
	case roms.ModelPlus2A:
		return m.setupPlus2A()
	case roms.ModelPlus3:
		return m.setupPlus3()
	default:
		return fmt.Errorf("unsupported model: %d", model)
	}
}

// setup48K configures memory for 48K model
func (m *Memory) setup48K() error {
	if rom, exists := m.romManager.GetROM(roms.ROM48K); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("48K ROM not found")
	}
	
	// 48K memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}  // ROM 0, RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM not writable
	m.PagingEnabled = false
	m.ScreenPage = 5
	return nil
}

// setup128K configures memory for 128K model
func (m *Memory) setup128K() error {
	if rom, exists := m.romManager.GetROM(roms.ROM128K_0); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("128K ROM 0 not found")
	}
	
	if rom, exists := m.romManager.GetROM(roms.ROM128K_1); exists {
		copy(m.rom[1], rom)
	} else {
		return fmt.Errorf("128K ROM 1 not found")
	}
	
	// 128K memory layout - paging must be enabled for 128K models
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}  // ROM 0, RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM not writable
	m.PagingEnabled = true   // 128K models have paging available
	m.ScreenPage = 5
	return nil
}

// setupPlus2 configures memory for +2 model
func (m *Memory) setupPlus2() error {
	if rom, exists := m.romManager.GetROM(roms.ROMPLUS2_0); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("+2 ROM 0 not found")
	}
	
	if rom, exists := m.romManager.GetROM(roms.ROMPLUS2_1); exists {
		copy(m.rom[1], rom)
	} else {
		return fmt.Errorf("+2 ROM 1 not found")
	}
	
	// +2 memory layout (similar to 128K)
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// setupPlus2A configures memory for +2A model
func (m *Memory) setupPlus2A() error {
	for i := 0; i < 4; i++ {
		romType := roms.ROMType(int(roms.ROMPLUS2A_0) + i)
		if rom, exists := m.romManager.GetROM(romType); exists {
			copy(m.rom[i], rom)
		} else {
			return fmt.Errorf("+2A ROM %d not found", i)
		}
	}
	
	// +2A memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// setupPlus3 configures memory for +3 model  
func (m *Memory) setupPlus3() error {
	for i := 0; i < 4; i++ {
		romType := roms.ROMType(int(roms.ROMPLUS3_0) + i)
		if rom, exists := m.romManager.GetROM(romType); exists {
			copy(m.rom[i], rom)
		} else {
			return fmt.Errorf("+3 ROM %d not found", i)
		}
	}
	
	// +3 memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// GetCurrentModel returns the current Spectrum model
func (m *Memory) GetCurrentModel() roms.SpectrumModel {
	return m.currentModel
}

// GetROMManager returns the ROM manager
func (m *Memory) GetROMManager() *roms.ROMManager {
	return m.romManager
}

// SwitchModel changes the current Spectrum model and reconfigures memory
func (m *Memory) SwitchModel(model roms.SpectrumModel) error {
	// Load ROMs for the new model if not already loaded
	if err := m.romManager.LoadROMsForModel(model); err != nil {
		return fmt.Errorf("failed to load ROMs for model %s: %w", roms.GetModelName(model), err)
	}
	
	m.currentModel = model
	return m.setupModel(model)
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

	// Bit 4: ROM select
	// For +3/+2A, the ROM index is formed from two bits:
	//   bit 4 of 0x7FFD (low bit) + bit 2 of 0x1FFD (high bit)
	// For 128K/+2, only bit 4 of 0x7FFD is used (ROM 0 or ROM 1)
	if m.currentModel == roms.ModelPlus3 || m.currentModel == roms.ModelPlus2A {
		romIndex := int((val >> 4) & 1)
		romIndex |= int((m.port1FFD >> 1) & 2) // bit 2 of 1FFD becomes bit 1 of index
		m.memoryPageReadMap[0] = 16 + romIndex
	} else {
		if (val & 0x10) != 0 {
			m.memoryPageReadMap[0] = 17 // ROM 1
		} else {
			m.memoryPageReadMap[0] = 16 // ROM 0
		}
	}

	// Bit 5: Paging disable
	if (val & 0x20) != 0 {
		m.PagingEnabled = false
	}
}

// PageMemoryPlus3 handles the +3/+2A special paging via port 0x1FFD.
func (m *Memory) PageMemoryPlus3(val byte) {
	if !m.PagingEnabled {
		return
	}
	m.port1FFD = val

	if val&0x01 != 0 {
		// Special paging mode: 4 predefined RAM configurations
		m.specialPaging = true
		mode := (val >> 1) & 0x03
		switch mode {
		case 0: // 0,1,2,3
			m.memoryPageReadMap = [4]int{0, 1, 2, 3}
			m.memoryPageWriteMap = [4]int{0, 1, 2, 3}
		case 1: // 4,5,6,7
			m.memoryPageReadMap = [4]int{4, 5, 6, 7}
			m.memoryPageWriteMap = [4]int{4, 5, 6, 7}
		case 2: // 4,5,6,3
			m.memoryPageReadMap = [4]int{4, 5, 6, 3}
			m.memoryPageWriteMap = [4]int{4, 5, 6, 3}
		case 3: // 4,7,6,3
			m.memoryPageReadMap = [4]int{4, 7, 6, 3}
			m.memoryPageWriteMap = [4]int{4, 7, 6, 3}
		}
	} else {
		// Normal paging mode — restore standard mapping
		m.specialPaging = false
		m.memoryPageWriteMap[0] = -1
		m.memoryPageReadMap[1] = 5
		m.memoryPageWriteMap[1] = 5
		m.memoryPageReadMap[2] = 2
		m.memoryPageWriteMap[2] = 2
		// Update ROM selection using both port values
		romIndex := int((m.port1FFD >> 1) & 2) // bit 2 of 1FFD → bit 1 of index
		// bit 4 of 7FFD will be applied by the next PageMemory call, but
		// set a reasonable default from the current mapping
		m.memoryPageReadMap[0] = 16 + romIndex
	}
}