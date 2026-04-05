package peripherals

import (
	"fmt"
	"log"

	"github.com/conorarmstrong/zx_go/pkg/disciple"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// PeripheralManager manages all peripheral devices
type PeripheralManager struct {
	memory    *memory.Memory
	disciple  *disciple.Disciple
	multiface *multiface.Multiface
	
	// Configuration
	discipleEnabled   bool
	multifaceEnabled  bool
	multifaceVariant  multiface.MultifaceType
}

// NewPeripheralManager creates a new peripheral manager
func NewPeripheralManager(mem *memory.Memory, romPath string) *PeripheralManager {
	pm := &PeripheralManager{
		memory:           mem,
		discipleEnabled:  false,
		multifaceEnabled: false,
		multifaceVariant: multiface.Multiface128, // Default variant
	}
	
	return pm
}

// EnableDisciple enables the Disciple disk interface
func (pm *PeripheralManager) EnableDisciple(romPath string) error {
	if pm.discipleEnabled {
		return nil // Already enabled
	}
	
	var err error
	pm.disciple, err = disciple.NewDisciple(romPath, pm.memory)
	if err != nil {
		return fmt.Errorf("failed to initialize Disciple: %w", err)
	}
	
	pm.discipleEnabled = true
	log.Println("Disciple disk interface enabled")
	return nil
}

// DisableDisciple disables the Disciple disk interface
func (pm *PeripheralManager) DisableDisciple() {
	pm.discipleEnabled = false
	pm.disciple = nil
	log.Println("Disciple disk interface disabled")
}

// EnableMultiface enables the Multiface interface
func (pm *PeripheralManager) EnableMultiface(variant multiface.MultifaceType, romPath string) error {
	if pm.multifaceEnabled && pm.multifaceVariant == variant {
		return nil // Already enabled with same variant
	}
	
	var err error
	pm.multiface, err = multiface.NewMultiface(variant, romPath, pm.memory)
	if err != nil {
		return fmt.Errorf("failed to initialize %s: %w", multiface.GetVariantName(variant), err)
	}
	
	pm.multifaceEnabled = true
	pm.multifaceVariant = variant
	log.Printf("%s enabled", multiface.GetVariantName(variant))
	return nil
}

// DisableMultiface disables the Multiface interface
func (pm *PeripheralManager) DisableMultiface() {
	pm.multifaceEnabled = false
	pm.multiface = nil
	log.Println("Multiface disabled")
}

// HandlePortRead handles I/O port reads from peripherals
func (pm *PeripheralManager) HandlePortRead(port uint16) (byte, bool) {
	// Check Disciple first (lower port addresses)
	if pm.discipleEnabled && pm.disciple != nil {
		if value, handled := pm.disciple.HandlePortRead(port); handled {
			return value, true
		}
	}
	
	// Check Multiface
	if pm.multifaceEnabled && pm.multiface != nil {
		if value, handled := pm.multiface.HandlePortRead(port); handled {
			return value, true
		}
	}
	
	return 0, false
}

// HandlePortWrite handles I/O port writes to peripherals
func (pm *PeripheralManager) HandlePortWrite(port uint16, value byte) bool {
	handled := false
	
	// Check Disciple first
	if pm.discipleEnabled && pm.disciple != nil {
		if pm.disciple.HandlePortWrite(port, value) {
			handled = true
		}
	}
	
	// Check Multiface (can coexist)
	if pm.multifaceEnabled && pm.multiface != nil {
		if pm.multiface.HandlePortWrite(port, value) {
			handled = true
		}
	}
	
	return handled
}

// HandleNMI handles Non-Maskable Interrupt (typically from Multiface red button)
func (pm *PeripheralManager) HandleNMI() bool {
	if pm.multifaceEnabled && pm.multiface != nil {
		return pm.multiface.HandleNMI()
	}
	
	return false
}

// HandleOpcodeRead handles opcode reads that might trigger peripheral actions
func (pm *PeripheralManager) HandleOpcodeRead(addr uint16) bool {
	if pm.multifaceEnabled && pm.multiface != nil {
		return pm.multiface.HandleOpcodeRead(addr)
	}
	
	return false
}

// HandleMemoryRead handles memory reads that might be intercepted by peripherals
func (pm *PeripheralManager) HandleMemoryRead(addr uint16) (byte, bool) {
	// Check if Multiface ROM is paged in
	if pm.multifaceEnabled && pm.multiface != nil && pm.multiface.IsROMPaged() {
		if addr < 0x2000 { // Multiface ROM area
			rom := pm.multiface.GetROM()
			return rom[addr], true
		}
	}
	
	// Check if Disciple ROM is paged in
	if pm.discipleEnabled && pm.disciple != nil && pm.disciple.IsROMPaged() {
		if addr < 0x2000 { // Disciple ROM area
			rom := pm.disciple.GetROM()
			return rom[addr], true
		}
	}
	
	return 0, false
}

// HandleMemoryWrite handles memory writes that might be intercepted by peripherals
func (pm *PeripheralManager) HandleMemoryWrite(addr uint16, value byte) bool {
	// Check if Multiface RAM is accessible
	if pm.multifaceEnabled && pm.multiface != nil && pm.multiface.IsROMPaged() {
		if addr >= 0x2000 && addr < 0x4000 { // Multiface RAM area (hypothetical)
			ram := pm.multiface.GetRAM()
			ram[addr-0x2000] = value
			return true
		}
	}
	
	// Check if Disciple RAM is accessible
	if pm.discipleEnabled && pm.disciple != nil && pm.disciple.IsROMPaged() {
		if addr >= 0x2000 && addr < 0x4000 { // Disciple RAM area (hypothetical)
			ram := pm.disciple.GetRAM()
			ram[addr-0x2000] = value
			return true
		}
	}
	
	return false
}

// GetStatus returns status information about enabled peripherals
func (pm *PeripheralManager) GetStatus() map[string]interface{} {
	status := make(map[string]interface{})
	
	status["disciple_enabled"] = pm.discipleEnabled
	if pm.discipleEnabled && pm.disciple != nil {
		status["disciple_rom_paged"] = pm.disciple.IsROMPaged()
		status["disciple_inhibited"] = pm.disciple.IsInhibited()
	}
	
	status["multiface_enabled"] = pm.multifaceEnabled
	if pm.multifaceEnabled && pm.multiface != nil {
		status["multiface_variant"] = multiface.GetVariantName(pm.multifaceVariant)
		status["multiface_rom_paged"] = pm.multiface.IsROMPaged()
		status["multiface_invisible"] = pm.multiface.IsInvisible()
		status["multiface_red_button"] = pm.multiface.IsRedButtonPressed()
	}
	
	return status
}

// IsDisciple enabled returns whether Disciple is enabled
func (pm *PeripheralManager) IsDiscipleEnabled() bool {
	return pm.discipleEnabled
}

// IsMultifaceEnabled returns whether Multiface is enabled
func (pm *PeripheralManager) IsMultifaceEnabled() bool {
	return pm.multifaceEnabled
}

// GetDisciple returns the Disciple interface (if enabled)
func (pm *PeripheralManager) GetDisciple() *disciple.Disciple {
	return pm.disciple
}

// GetMultiface returns the Multiface interface (if enabled)
func (pm *PeripheralManager) GetMultiface() *multiface.Multiface {
	return pm.multiface
}

// LoadDiscipleDisk loads a disk image into the Disciple interface
func (pm *PeripheralManager) LoadDiscipleDisk(drive int, filename string) error {
	if !pm.discipleEnabled || pm.disciple == nil {
		return fmt.Errorf("disciple not enabled")
	}
	
	return pm.disciple.LoadDisk(drive, filename)
}

// SaveMultifaceSnapshot saves a snapshot using the Multiface
func (pm *PeripheralManager) SaveMultifaceSnapshot(filename string) error {
	if !pm.multifaceEnabled || pm.multiface == nil {
		return fmt.Errorf("multiface not enabled")
	}
	
	return pm.multiface.SaveSnapshot(filename)
}

// LoadMultifaceSnapshot loads a snapshot using the Multiface
func (pm *PeripheralManager) LoadMultifaceSnapshot(filename string) error {
	if !pm.multifaceEnabled || pm.multiface == nil {
		return fmt.Errorf("multiface not enabled")
	}
	
	return pm.multiface.LoadSnapshot(filename)
}

// SetDiscipleInhibit sets the Disciple inhibit state
func (pm *PeripheralManager) SetDiscipleInhibit(inhibit bool) {
	if pm.discipleEnabled && pm.disciple != nil {
		pm.disciple.SetInhibit(inhibit)
	}
}

// SetMultifaceEnabled sets the Multiface enabled state
func (pm *PeripheralManager) SetMultifaceEnabled(enabled bool) {
	if pm.multifaceEnabled && pm.multiface != nil {
		pm.multiface.SetEnabled(enabled)
	}
}

// GetCompatibleMultifaceVariant returns the compatible Multiface variant for current model
func (pm *PeripheralManager) GetCompatibleMultifaceVariant(model roms.SpectrumModel) multiface.MultifaceType {
	switch model {
	case roms.ModelPlus3:
		return multiface.Multiface3
	case roms.Model128K, roms.ModelPlus2, roms.ModelPlus2A:
		return multiface.Multiface128
	default:
		return multiface.Multiface1
	}
}