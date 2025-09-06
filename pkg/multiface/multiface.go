package multiface

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// MultifaceType represents different Multiface variants
type MultifaceType int

const (
	Multiface1 MultifaceType = iota
	Multiface128
	Multiface3
)

// Multiface represents the Romantic Robot Multiface interface
type Multiface struct {
	// ROM data (8KB)
	rom []byte
	
	// RAM data (8KB) 
	ram []byte
	
	// Current variant
	variant MultifaceType
	
	// Control state
	enabled     bool
	romPaged    bool
	redButton   bool
	invisible   bool // Stealth mode
	
	// Video page store (for Multiface 128/3)
	videoPageStore byte
	
	// Memory reference for paging
	memory *memory.Memory
	
	// ROM filename for loading
	romFile string
}

// Port ranges for Multiface I/O decoding
const (
	// Port decoding: xxxx xxxx x011 x1xx (A2, A4-A5, IORQ, RD/WR)
	PortBase     = 0x3C  // Base port address pattern
	PortMask     = 0x3C  // Mask for port address matching
	
	// Video page port: 0xxx xxxx xxxx xx0x (A1, A15, WR)
	VideoPortBase = 0x0000
	VideoPortMask = 0x8002
)

// NMI vector addresses that trigger Multiface activation
const (
	NMIVector1 = 0x0066
	NMIVector2 = 0x0067
)

// NewMultiface creates a new Multiface interface
func NewMultiface(variant MultifaceType, romPath string, memory *memory.Memory) (*Multiface, error) {
	mf := &Multiface{
		ram:     make([]byte, 0x2000), // 8KB RAM
		variant: variant,
		memory:  memory,
	}
	
	// Set ROM filename based on variant
	switch variant {
	case Multiface1:
		mf.romFile = "mf1_official.rom"
	case Multiface128:
		mf.romFile = "mf128_official.rom"
	case Multiface3:
		mf.romFile = "mf3_official.rom"
	default:
		mf.romFile = "mf1_official.rom"
	}
	
	// Load ROM
	if err := mf.loadROM(romPath); err != nil {
		return nil, fmt.Errorf("failed to load Multiface ROM: %w", err)
	}
	
	mf.reset()
	return mf, nil
}

// loadROM loads the Multiface ROM
func (mf *Multiface) loadROM(romPath string) error {
	// Try the specific ROM file first
	path := filepath.Join(romPath, mf.romFile)
	
	if data, err := ioutil.ReadFile(path); err == nil {
		if len(data) == 0x2000 { // 8KB ROM
			mf.rom = data
			return nil
		}
	}
	
	// Try alternative names
	altNames := []string{"mf128.rom", "mf3.rom", "multiface.rom"}
	for _, name := range altNames {
		path := filepath.Join(romPath, name)
		if data, err := ioutil.ReadFile(path); err == nil {
			if len(data) == 0x2000 {
				mf.rom = data
				return nil
			}
		}
	}
	
	// Create a minimal placeholder ROM if no real ROM found
	mf.rom = make([]byte, 0x2000)
	// Add basic Multiface signature - jump to main code
	copy(mf.rom[0:], []byte{0xF3, 0xC3, 0x10, 0x00}) // DI, JP 0x0010
	
	// Add some basic Multiface functionality at 0x0010
	mf.rom[0x0010] = 0x3E // LD A, version
	switch mf.variant {
	case Multiface128:
		mf.rom[0x0011] = 0x02 // Version 2 for MF128
	case Multiface3:
		mf.rom[0x0011] = 0x03 // Version 3 for MF3
	default:
		mf.rom[0x0011] = 0x01 // Version 1 for MF1
	}
	mf.rom[0x0012] = 0xC9 // RET
	
	fmt.Printf("Warning: Using placeholder %s ROM - functionality will be limited\n", mf.romFile)
	
	return nil
}

// Reset resets the Multiface to initial state
func (mf *Multiface) reset() {
	mf.enabled = true
	mf.romPaged = false
	mf.redButton = false
	mf.invisible = false
	mf.videoPageStore = 0
}

// HandleNMI handles Non-Maskable Interrupt (red button press)
func (mf *Multiface) HandleNMI() bool {
	if !mf.enabled || mf.invisible {
		return false
	}
	
	mf.redButton = true
	mf.pageInROM()
	
	// Return true to indicate NMI was handled by Multiface
	return true
}

// HandleOpcodeRead handles opcode fetch that might trigger ROM paging
func (mf *Multiface) HandleOpcodeRead(addr uint16) bool {
	if !mf.enabled || mf.invisible || !mf.redButton {
		return false
	}
	
	// Check if PC is at NMI vector (0x0066 or 0x0067)
	if addr == NMIVector1 || addr == NMIVector2 {
		mf.pageInROM()
		return true
	}
	
	return false
}

// HandlePortRead handles reads from Multiface I/O ports
func (mf *Multiface) HandlePortRead(port uint16) (byte, bool) {
	if !mf.enabled || mf.invisible {
		return 0, false
	}
	
	// Check if this is a Multiface port (xxxx xxxx x011 x1xx)
	if (port & PortMask) == PortBase {
		// Return some status information
		status := byte(0)
		if mf.romPaged {
			status |= 0x01
		}
		if mf.redButton {
			status |= 0x02
		}
		return status, true
	}
	
	return 0, false
}

// HandlePortWrite handles writes to Multiface I/O ports
func (mf *Multiface) HandlePortWrite(port uint16, value byte) bool {
	if !mf.enabled {
		return false
	}
	
	// Check if this is a Multiface port (xxxx xxxx x011 x1xx)
	if (port & PortMask) == PortBase {
		mf.handleControlWrite(value)
		return true
	}
	
	// Check for video page store (for Multiface 128/3)
	if mf.variant == Multiface128 || mf.variant == Multiface3 {
		// Video page port: 0xxx xxxx xxxx xx0x (A1, A15, WR)
		if (port & VideoPortMask) == VideoPortBase {
			mf.videoPageStore = (value >> 3) & 0x01 // Store bit 3
			return true
		}
	}
	
	return false
}

// handleControlWrite handles control register writes
func (mf *Multiface) handleControlWrite(value byte) {
	// Bit 0: ROM page out
	if value&0x01 != 0 {
		mf.pageOutROM()
	}
	
	// Bit 1: Clear red button
	if value&0x02 != 0 {
		mf.redButton = false
	}
	
	// Bit 2: Invisible mode (stealth)
	if value&0x04 != 0 {
		mf.invisible = true
		mf.pageOutROM()
	}
	
	// Bit 3: Visible mode  
	if value&0x08 != 0 {
		mf.invisible = false
	}
}

// pageInROM pages in the Multiface ROM
func (mf *Multiface) pageInROM() {
	if mf.invisible || !mf.enabled {
		return
	}
	
	// In a real implementation, this would modify the memory map
	// to page in the Multiface ROM at 0x0000-0x1FFF
	mf.romPaged = true
}

// pageOutROM pages out the Multiface ROM
func (mf *Multiface) pageOutROM() {
	mf.romPaged = false
}

// IsEnabled returns whether the Multiface is enabled
func (mf *Multiface) IsEnabled() bool {
	return mf.enabled
}

// IsROMPaged returns whether the Multiface ROM is paged in
func (mf *Multiface) IsROMPaged() bool {
	return mf.romPaged
}

// IsInvisible returns whether the Multiface is in stealth mode
func (mf *Multiface) IsInvisible() bool {
	return mf.invisible
}

// IsRedButtonPressed returns whether the red button is pressed
func (mf *Multiface) IsRedButtonPressed() bool {
	return mf.redButton
}

// GetVariant returns the Multiface variant
func (mf *Multiface) GetVariant() MultifaceType {
	return mf.variant
}

// GetROM returns the Multiface ROM data
func (mf *Multiface) GetROM() []byte {
	return mf.rom
}

// GetRAM returns the Multiface RAM data
func (mf *Multiface) GetRAM() []byte {
	return mf.ram
}

// SetEnabled sets the enabled state
func (mf *Multiface) SetEnabled(enabled bool) {
	mf.enabled = enabled
	if !enabled {
		mf.pageOutROM()
		mf.redButton = false
	}
}

// GetVariantName returns a human-readable name for the variant
func GetVariantName(variant MultifaceType) string {
	switch variant {
	case Multiface128:
		return "Multiface 128"
	case Multiface3:
		return "Multiface 3"
	default:
		return "Multiface 1"
	}
}

// SaveSnapshot saves a memory snapshot (placeholder implementation)
func (mf *Multiface) SaveSnapshot(filename string) error {
	if !mf.enabled || !mf.romPaged {
		return fmt.Errorf("Multiface not active")
	}
	
	// In a real implementation, this would save the entire Spectrum memory
	// as a snapshot file (.sna, .z80, etc.)
	
	return fmt.Errorf("snapshot saving not yet implemented")
}

// LoadSnapshot loads a memory snapshot (placeholder implementation)
func (mf *Multiface) LoadSnapshot(filename string) error {
	if !mf.enabled {
		return fmt.Errorf("Multiface not enabled")
	}
	
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return fmt.Errorf("snapshot file not found: %s", filename)
	}
	
	// In a real implementation, this would load a snapshot file
	// and restore the Spectrum memory state
	
	return fmt.Errorf("snapshot loading not yet implemented")
}

// GetCompatibleModel returns the compatible Spectrum model for this Multiface
func (mf *Multiface) GetCompatibleModel() roms.SpectrumModel {
	switch mf.variant {
	case Multiface3:
		return roms.ModelPlus3
	case Multiface128:
		return roms.Model128K
	default:
		return roms.Model48K
	}
}