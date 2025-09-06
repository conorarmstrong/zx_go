package roms

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

// ROMType represents different types of ROMs
type ROMType int

const (
	ROM48K ROMType = iota
	ROM128K_0
	ROM128K_1
	ROMPLUS2_0
	ROMPLUS2_1
	ROMPLUS2A_0
	ROMPLUS2A_1
	ROMPLUS2A_2
	ROMPLUS2A_3
	ROMPLUS3_0
	ROMPLUS3_1
	ROMPLUS3_2
	ROMPLUS3_3
	ROMMULTIFACE1
	ROMMULTIFACE128
	ROMMULTIFACE3
	ROMDISCIPLE
	ROMTRDOS
	ROMPENTAGON
)

// SpectrumModel represents different ZX Spectrum models
type SpectrumModel int

const (
	Model48K SpectrumModel = iota
	Model128K
	ModelPlus2
	ModelPlus2A
	ModelPlus3
)

// ROMMappingEntry defines how ROMs are mapped in memory
type ROMMappingEntry struct {
	ROMType   ROMType
	Filename  string
	Required  bool
	PageIndex int
}

// ROMManager handles ROM loading and management for different Spectrum models
type ROMManager struct {
	roms     map[ROMType][]byte
	romPath  string
	mappings map[SpectrumModel][]ROMMappingEntry
}

// NewROMManager creates a new ROM manager
func NewROMManager(romPath string) *ROMManager {
	rm := &ROMManager{
		roms:    make(map[ROMType][]byte),
		romPath: romPath,
	}
	rm.initMappings()
	return rm
}

// initMappings defines ROM mappings for different Spectrum models
func (rm *ROMManager) initMappings() {
	rm.mappings = map[SpectrumModel][]ROMMappingEntry{
		Model48K: {
			{ROM48K, "48.rom", true, 0},
		},
		Model128K: {
			{ROM128K_0, "128-0.rom", true, 0},
			{ROM128K_1, "128-1.rom", true, 1},
		},
		ModelPlus2: {
			{ROMPLUS2_0, "plus2-0.rom", true, 0},
			{ROMPLUS2_1, "plus2-1.rom", true, 1},
		},
		ModelPlus2A: {
			{ROMPLUS2A_0, "plus3-0.rom", true, 0}, // Use +3 ROMs for +2A (they're compatible)
			{ROMPLUS2A_1, "plus3-1.rom", true, 1},
			{ROMPLUS2A_2, "plus3-2.rom", true, 2},
			{ROMPLUS2A_3, "plus3-3.rom", true, 3},
		},
		ModelPlus3: {
			{ROMPLUS3_0, "plus3-0.rom", true, 0},
			{ROMPLUS3_1, "plus3-1.rom", true, 1},
			{ROMPLUS3_2, "plus3-2.rom", true, 2},
			{ROMPLUS3_3, "plus3-3.rom", true, 3},
		},
	}
}

// LoadROM loads a single ROM file
func (rm *ROMManager) LoadROM(romType ROMType, filename string) error {
	romPath := filepath.Join(rm.romPath, filename)
	
	// Check if file exists
	if _, err := os.Stat(romPath); os.IsNotExist(err) {
		return fmt.Errorf("ROM file %s does not exist", filename)
	}
	
	data, err := ioutil.ReadFile(romPath)
	if err != nil {
		return fmt.Errorf("failed to load ROM %s: %w", filename, err)
	}
	
	// Ensure ROM is exactly 16KB
	if len(data) != 16384 {
		return fmt.Errorf("ROM %s has invalid size: %d bytes (expected 16384)", filename, len(data))
	}
	
	rm.roms[romType] = data
	return nil
}

// LoadROMsForModel loads all ROMs required for a specific Spectrum model
func (rm *ROMManager) LoadROMsForModel(model SpectrumModel) error {
	mappings, exists := rm.mappings[model]
	if !exists {
		return fmt.Errorf("unsupported Spectrum model: %d", model)
	}
	
	for _, mapping := range mappings {
		if err := rm.LoadROM(mapping.ROMType, mapping.Filename); err != nil {
			if mapping.Required {
				return err
			}
			// Optional ROM, just log and continue
			fmt.Printf("Warning: Optional ROM %s not found: %v\n", mapping.Filename, err)
		}
	}
	
	return nil
}

// LoadPeripheralROMs loads ROMs for peripheral devices
func (rm *ROMManager) LoadPeripheralROMs() error {
	peripheralROMs := map[ROMType]string{
		ROMMULTIFACE1:   "mf1_official.rom",
		ROMMULTIFACE128: "mf128_official.rom",
		ROMMULTIFACE3:   "mf3_official.rom",
		ROMDISCIPLE:     "gdos.rom",
		ROMTRDOS:        "trdos.rom",
		ROMPENTAGON:     "pentagon.rom",
	}
	
	for romType, filename := range peripheralROMs {
		if err := rm.LoadROM(romType, filename); err != nil {
			fmt.Printf("Warning: Peripheral ROM %s not found: %v\n", filename, err)
		}
	}
	
	return nil
}

// GetROM returns the data for a specific ROM type
func (rm *ROMManager) GetROM(romType ROMType) ([]byte, bool) {
	rom, exists := rm.roms[romType]
	return rom, exists
}

// HasROM checks if a ROM is loaded
func (rm *ROMManager) HasROM(romType ROMType) bool {
	_, exists := rm.roms[romType]
	return exists
}

// GetLoadedROMs returns a list of all loaded ROM types
func (rm *ROMManager) GetLoadedROMs() []ROMType {
	var loaded []ROMType
	for romType := range rm.roms {
		loaded = append(loaded, romType)
	}
	return loaded
}

// GetModelName returns a human-readable name for a Spectrum model
func GetModelName(model SpectrumModel) string {
	switch model {
	case Model48K:
		return "ZX Spectrum 48K"
	case Model128K:
		return "ZX Spectrum 128K"
	case ModelPlus2:
		return "ZX Spectrum +2"
	case ModelPlus2A:
		return "ZX Spectrum +2A"
	case ModelPlus3:
		return "ZX Spectrum +3"
	default:
		return "Unknown Model"
	}
}

// GetROMTypeName returns a human-readable name for a ROM type
func GetROMTypeName(romType ROMType) string {
	switch romType {
	case ROM48K:
		return "48K ROM"
	case ROM128K_0:
		return "128K ROM 0"
	case ROM128K_1:
		return "128K ROM 1"
	case ROMPLUS2_0:
		return "+2 ROM 0"
	case ROMPLUS2_1:
		return "+2 ROM 1"
	case ROMPLUS2A_0:
		return "+2A ROM 0"
	case ROMPLUS2A_1:
		return "+2A ROM 1"
	case ROMPLUS2A_2:
		return "+2A ROM 2"
	case ROMPLUS2A_3:
		return "+2A ROM 3"
	case ROMPLUS3_0:
		return "+3 ROM 0"
	case ROMPLUS3_1:
		return "+3 ROM 1"
	case ROMPLUS3_2:
		return "+3 ROM 2"
	case ROMPLUS3_3:
		return "+3 ROM 3"
	case ROMMULTIFACE1:
		return "Multiface 1"
	case ROMMULTIFACE128:
		return "Multiface 128"
	case ROMMULTIFACE3:
		return "Multiface 3"
	case ROMDISCIPLE:
		return "Disciple/GDOS"
	case ROMTRDOS:
		return "TR-DOS"
	case ROMPENTAGON:
		return "Pentagon"
	default:
		return "Unknown ROM"
	}
}