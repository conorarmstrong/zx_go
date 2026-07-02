package roms

import (
	"fmt"
	"log/slog"
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
	ROMINTERFACE1 // 8 KB Sinclair Interface 1 shadow ROM (if1-2.rom)
	ROMZX81       // 8 KB Sinclair ZX81 ROM
	ROMZX80       // 4 KB Sinclair ZX80 ROM
)

// SpectrumModel represents different ZX Spectrum models
type SpectrumModel int

const (
	Model48K SpectrumModel = iota
	Model128K
	ModelPlus2
	ModelPlus2A
	ModelPlus3
	// ModelNext is the ZX Spectrum Next. Its ROMs are user-installed
	// at runtime rather than mapped here — see the empty ModelNext
	// entry in initMappings for why.
	ModelNext
	// ModelZX81 is the Sinclair ZX81 (8 KB ROM, 1–16 KB RAM, CPU-generated
	// display). ModelZX80 is the earlier ZX80 (4 KB ROM, no SLOW mode).
	// Both share the Z80 and the pkg/zx8x video mechanism. Appended after
	// ModelNext so the existing models keep their iota values.
	ModelZX81
	ModelZX80
	// ModelPentagon is the Soviet ZX Spectrum 128 clone: 128K paging and AY
	// like the 128K, but with no memory contention and a longer 71680-T frame.
	ModelPentagon
	// ModelSAM is the MGT SAM Coupé (1989): a Z80B at 6 MHz with a custom ASIC
	// (four screen modes, 128-colour palette), 256/512K paged memory, an SAA1099
	// stereo sound chip and a WD1772 floppy controller. Self-contained in
	// pkg/sam (like pkg/zx8x). Appended last to keep existing iota values stable.
	ModelSAM
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
		// ModelNext intentionally has no embedded ROM mappings:
		// the NextZXOS distro ROM, divMMC ROM and Alt-ROM are
		// user-installed at runtime for licensing reasons. An empty slice
		// here makes LoadROMsForModel(ModelNext) a no-op success
		// instead of returning "unsupported model".
		ModelNext: {},
		ModelZX81: {
			{ROMZX81, "zx81.rom", true, 0},
		},
		ModelZX80: {
			{ROMZX80, "zx80.rom", true, 0},
		},
		// Pentagon 128: bank 0 is the Pentagon 128 editor ROM; bank 1 is the
		// standard 48 BASIC (shared with the 128K — Pentagon doesn't modify it).
		ModelPentagon: {
			{ROMPENTAGON, "pentagon.rom", true, 0},
			{ROM128K_1, "128-1.rom", true, 1},
		},
	}
}

// LoadROM loads a single ROM file. It tries the embedded ROMs first,
// then falls back to the filesystem path.
func (rm *ROMManager) LoadROM(romType ROMType, filename string) error {
	var data []byte
	var err error

	// Try filesystem first (allows user overrides), then fall back to embedded ROMs
	romPath := filepath.Join(rm.romPath, filename)
	data, err = os.ReadFile(romPath)
	if err != nil {
		// Fall back to embedded ROMs
		data, err = embeddedROMs.ReadFile("data/" + filename)
		if err != nil {
			return fmt.Errorf("rom file %s not found", filename)
		}
	}

	// Determine expected size based on ROM type
	var expectedSize int
	switch romType {
	case ROMMULTIFACE1, ROMMULTIFACE128, ROMMULTIFACE3, ROMDISCIPLE, ROMINTERFACE1, ROMZX81:
		expectedSize = 8192 // 8KB (peripherals + ZX81)
	case ROMZX80:
		expectedSize = 4096 // 4KB ZX80 ROM
	default:
		expectedSize = 16384 // 16KB for system ROMs
	}

	if len(data) != expectedSize {
		return fmt.Errorf("rom %s has invalid size: %d bytes (expected %d)", filename, len(data), expectedSize)
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
			slog.Warn("optional ROM not loaded", "filename", mapping.Filename, "err", err)
		}
	}

	return nil
}

// LoadPeripheralROMs loads ROMs for peripheral devices.
//
// Each ROM is treated as optional: if the file isn't present in the
// user's roms/ directory or in the embedded ROMs, we log a warning
// and continue. The peripheral that depends on the missing ROM will
// then refuse to enable when the user tries to turn it on, with an
// error message that points at the expected filename.
func (rm *ROMManager) LoadPeripheralROMs() error {
	peripheralROMs := map[ROMType]string{
		ROMMULTIFACE1:   "mf1_official.rom",
		ROMMULTIFACE128: "mf128_official.rom",
		ROMMULTIFACE3:   "mf3_official.rom",
		ROMDISCIPLE:     "gdos.rom",
		ROMTRDOS:        "trdos.rom",
		ROMPENTAGON:     "pentagon.rom",
		// The Interface 1 ROM (if1-2.rom) is widely available from
		// World of Spectrum and similar archives but copyrighted by
		// Sinclair, so we don't ship it embedded. Users who want
		// the Interface 1 / Microdrive functionality drop the file
		// in their roms/ directory.
		ROMINTERFACE1: "if1-2.rom",
	}

	for romType, filename := range peripheralROMs {
		if err := rm.LoadROM(romType, filename); err != nil {
			slog.Warn("peripheral ROM not loaded", "filename", filename, "err", err)
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
	case ModelNext:
		return "ZX Spectrum Next"
	case ModelZX81:
		return "Sinclair ZX81"
	case ModelZX80:
		return "Sinclair ZX80"
	case ModelPentagon:
		return "Pentagon 128"
	case ModelSAM:
		return "SAM Coupé"
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
	case ROMINTERFACE1:
		return "Interface 1"
	case ROMZX81:
		return "ZX81 ROM"
	case ROMZX80:
		return "ZX80 ROM"
	default:
		return "Unknown ROM"
	}
}
