package peripherals

import (
	"fmt"
	"log"

	"github.com/conorarmstrong/zx_go/pkg/disciple"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// PeripheralManager manages all peripheral devices
type PeripheralManager struct {
	memory    *memory.Memory
	disciple  *disciple.Disciple
	multiface *multiface.Multiface
	plus3fdc  *plus3fdc.Plus3FDC

	// Configuration
	discipleEnabled  bool
	multifaceEnabled bool
	multifaceVariant multiface.MultifaceType
}

// NewPeripheralManager creates a new peripheral manager
func NewPeripheralManager(mem *memory.Memory, romPath string) *PeripheralManager {
	pm := &PeripheralManager{
		memory:           mem,
		discipleEnabled:  false,
		multifaceEnabled: false,
		multifaceVariant: multiface.Multiface128, // Default variant
		// The +3 FDC is built once and always present — it only
		// responds to ports 0x2FFD/0x3FFD which are inert on
		// non-+3/+2A models, so leaving it always-on is harmless on
		// 48K/128K and avoids per-model wiring churn.
		plus3fdc: plus3fdc.New(),
	}

	return pm
}

// LoadPlus3Disk parses a DSK image and attaches it to the given drive of
// the +3 FDC (0 = A, 1 = B).
func (pm *PeripheralManager) LoadPlus3Disk(drive int, path string) error {
	return pm.plus3fdc.LoadDisk(drive, path)
}

// EjectPlus3Disk removes the disk from the given drive.
func (pm *PeripheralManager) EjectPlus3Disk(drive int) {
	pm.plus3fdc.EjectDisk(drive)
}

// SavePlus3Disk writes the disk in the given drive back to a DSK file.
func (pm *PeripheralManager) SavePlus3Disk(drive int, path string) error {
	return pm.plus3fdc.SaveDisk(drive, path)
}

// SetPlus3WriteProtect toggles the per-drive write-protect flag.
func (pm *PeripheralManager) SetPlus3WriteProtect(drive int, wp bool) {
	pm.plus3fdc.SetWriteProtect(drive, wp)
}

// SetPlus3Speedlock toggles the Speedlock copy-protection workaround on
// the +3 FDC. Enable this for games that use Speedlock; leave it off
// for normal software so legitimate BDOS retries aren't affected.
func (pm *PeripheralManager) SetPlus3Speedlock(enabled bool) {
	pm.plus3fdc.SetSpeedlockEnabled(enabled)
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

	// +3 FDC is gated on the model so 48K-only games can't accidentally
	// hit the disk controller via floating-bus reads at 0x2FFD/0x3FFD.
	if pm.plus3fdc != nil && pm.modelHasFDC() {
		if value, handled := pm.plus3fdc.HandlePortRead(port); handled {
			return value, true
		}
	}

	return 0, false
}

// modelHasFDC reports whether the current Spectrum model has a +3 FDC
// fitted (i.e. the +3 or the +2A).
func (pm *PeripheralManager) modelHasFDC() bool {
	switch pm.memory.GetCurrentModel() {
	case roms.ModelPlus3, roms.ModelPlus2A:
		return true
	}
	return false
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

	// +3 FDC writes (gated on the model — see HandlePortRead).
	if pm.plus3fdc != nil && pm.modelHasFDC() {
		if pm.plus3fdc.HandlePortWrite(port, value) {
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
	// Check if Multiface ROM/RAM is paged in
	if pm.multifaceEnabled && pm.multiface != nil && pm.multiface.IsROMPaged() {
		if addr < 0x2000 { // Multiface ROM area (0x0000-0x1FFF)
			rom := pm.multiface.GetROM()
			return rom[addr], true
		}
		if addr >= 0x2000 && addr < 0x4000 { // Multiface RAM area (0x2000-0x3FFF)
			ram := pm.multiface.GetRAM()
			return ram[addr-0x2000], true
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