package multiface

import (
	"fmt"
	"log"
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
	enabled   bool
	romPaged  bool
	redButton bool
	invisible bool // Stealth mode

	// Memory reference for paging
	memory *memory.Memory

	// ROM filename for loading
	romFile string

	// Session state: active from NMI until OUT re-arms the hardware
	sessionActive bool

	// savedPagingState is captured when the MF ROM pages in and restored when
	// it pages out, so that port writes the MF ROM code makes to
	// 0x7FFD/0x1FFD don't corrupt the host machine's paging configuration.
	savedPagingState *memory.PagingState
}

// Port ranges for Multiface I/O decoding.
// MF1:   port & 0x0072 == 0x0012  (xxxx xxxx x001 xx1x)
// MF128: port & 0x0072 == 0x0032  (xxxx xxxx x011 xx1x)
// MF3:   port & 0x0072 == 0x0032  (xxxx xxxx x011 xx1x)
// Page-in/out is controlled by A7 of the port address on IN instructions.
const (
	PortMask1    = 0x0072 // MF1 port mask
	PortValue1   = 0x0012 // MF1 port value
	PortMask128  = 0x0072 // MF128/MF3 port mask
	PortValue128 = 0x0032 // MF128/MF3 port value
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
	// Try filesystem first
	path := filepath.Join(romPath, mf.romFile)
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == 0x2000 {
			mf.rom = data
			return nil
		}
	}

	// Try embedded ROMs
	if data, err := roms.ReadEmbeddedROM(mf.romFile); err == nil {
		if len(data) == 0x2000 {
			mf.rom = data
			return nil
		}
	}

	// Try alternative filesystem names
	altNames := []string{"multiface1.rom", "multiface128.rom", "multiface3.rom"}
	for _, name := range altNames {
		if data, err := os.ReadFile(filepath.Join(romPath, name)); err == nil {
			if len(data) == 0x2000 {
				mf.rom = data
				return nil
			}
		}
		if data, err := roms.ReadEmbeddedROM(name); err == nil {
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

	log.Printf("Warning: Using placeholder %s ROM - functionality will be limited\n", mf.romFile)

	return nil
}

// Reset resets the Multiface to initial state
func (mf *Multiface) reset() {
	mf.enabled = true
	mf.romPaged = false
	mf.redButton = false
	mf.invisible = false
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

// isMultifacePort checks if the port matches this Multiface variant's decode.
// MF128 also responds to MF1's port pattern for backwards compatibility.
func (mf *Multiface) isMultifacePort(port uint16) bool {
	switch mf.variant {
	case Multiface1:
		return (port & PortMask1) == PortValue1
	case Multiface128:
		return (port&PortMask128) == PortValue128 || (port&PortMask1) == PortValue1
	default: // MF3
		return (port & PortMask128) == PortValue128
	}
}

// HandlePortRead handles reads from Multiface I/O ports.
// On real hardware, IN to the Multiface port controls page-in/page-out
// based on A7: A7=1 pages in (MF1/128) or out (MF3), A7=0 does the opposite.
func (mf *Multiface) HandlePortRead(port uint16) (byte, bool) {
	if !mf.enabled {
		return 0, false
	}
	if !mf.isMultifacePort(port) {
		return 0, false
	}

	a7 := (port & 0x0080) != 0

	switch mf.variant {
	case Multiface1, Multiface128:
		if a7 {
			if !mf.invisible {
				mf.pageInROM()
			}
		} else {
			mf.pageOutROM()
		}
	case Multiface3:
		// MF3 is inverted: A7=1 pages out, A7=0 pages in
		if a7 {
			mf.pageOutROM()
		} else if !mf.invisible {
			mf.pageInROM()
		}
	}

	return 0xFF, true
}

// HandlePortWrite handles writes to Multiface I/O ports.
// OUT to the Multiface port re-arms the hardware (IC8b_Q = 1).
// For MF128/MF3, A7 also controls the software lockout (J2).
func (mf *Multiface) HandlePortWrite(port uint16, value byte) bool {
	if !mf.enabled {
		return false
	}
	if !mf.isMultifacePort(port) {
		return false
	}

	// For MF128/MF3: A7 controls software lockout (J2/invisible)
	if mf.variant != Multiface1 {
		mf.invisible = (port & 0x0080) == 0
	}

	// OUT re-arms the hardware (IC8b_Q = 1).
	// This ends the MF session and restores the host paging state.
	if mf.sessionActive {
		mf.endSession()
	} else {
		mf.redButton = false
	}

	return true
}

// pageInROM pages in the Multiface ROM overlay.
func (mf *Multiface) pageInROM() {
	if mf.invisible || !mf.enabled {
		return
	}
	// On first page-in of a session, save the host paging state and
	// freeze RAM/screen paging. ROM selection stays unfrozen so the
	// MF code can access the Spectrum ROM's print routines.
	if !mf.sessionActive {
		mf.sessionActive = true
		mf.savedPagingState = mf.memory.SavePagingState()
		mf.memory.PagingFrozen = true
	}
	mf.romPaged = true
}

// pageOutROM removes the Multiface ROM overlay.
// Called during both the print cycle (temporary) and final exit.
func (mf *Multiface) pageOutROM() {
	mf.romPaged = false
}

// endSession restores the host paging state and ends the MF session.
// NOTE: does NOT change romPaged — the MF code's IN instruction will
// page out the ROM separately after it finishes its exit sequence.
func (mf *Multiface) endSession() {
	mf.sessionActive = false
	mf.redButton = false
	mf.memory.PagingFrozen = false
	if mf.savedPagingState != nil {
		mf.memory.RestorePagingState(mf.savedPagingState)
		mf.savedPagingState = nil
	}
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
		mf.romPaged = false
		if mf.sessionActive {
			mf.endSession()
		} else {
			mf.redButton = false
		}
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
