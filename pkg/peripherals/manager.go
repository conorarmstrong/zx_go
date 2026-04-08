package peripherals

import (
	"fmt"
	"log"

	"github.com/conorarmstrong/zx_go/pkg/disciple"
	"github.com/conorarmstrong/zx_go/pkg/if1"
	"github.com/conorarmstrong/zx_go/pkg/kempmouse"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/microdrive"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/zxprinter"
)

// PeripheralManager manages all peripheral devices
type PeripheralManager struct {
	memory    *memory.Memory
	disciple  *disciple.Disciple
	multiface *multiface.Multiface
	plus3fdc  *plus3fdc.Plus3FDC
	if1       *if1.IF1
	kempmouse *kempmouse.Mouse
	zxprinter *zxprinter.Printer

	// Configuration
	discipleEnabled  bool
	multifaceEnabled bool
	multifaceVariant multiface.MultifaceType
	if1Enabled       bool
	kempmouseEnabled bool
	zxprinterEnabled bool
}

// currentTstates returns the CPU's current T-state counter via the
// memory package's shared TStates pointer (set by the CPU at
// construction for memory contention). Used by peripherals like the
// ZX Printer that need cycle-accurate drum timing.
func (pm *PeripheralManager) currentTstates() int64 {
	if pm.memory.TStates == nil {
		return 0
	}
	return int64(*pm.memory.TStates)
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

// modelHasIF1 reports whether the current Spectrum model can have an
// Interface 1 attached. The IF1 was a 48K-only peripheral — its edge
// connector and shadow ROM were never adapted to the 128K series.
func (pm *PeripheralManager) modelHasIF1() bool {
	return pm.memory.GetCurrentModel() == roms.Model48K
}

// EnableInterface1 attaches an Interface 1 with the supplied ROM
// image bytes. Allowed only on the 48K Spectrum.
func (pm *PeripheralManager) EnableInterface1(romBytes []byte) error {
	if !pm.modelHasIF1() {
		return fmt.Errorf("interface 1 is only supported on the 48K Spectrum")
	}
	if pm.if1Enabled {
		return nil
	}
	dev := if1.New()
	if err := dev.LoadROMBytes(romBytes); err != nil {
		return fmt.Errorf("if1: load ROM: %w", err)
	}
	pm.if1 = dev
	pm.if1Enabled = true
	log.Println("Interface 1 enabled")
	return nil
}

// DisableInterface1 detaches the Interface 1 — any inserted
// microdrive cartridges are dropped.
func (pm *PeripheralManager) DisableInterface1() {
	pm.if1Enabled = false
	pm.if1 = nil
	log.Println("Interface 1 disabled")
}

// Frame ticks any peripheral that needs a per-frame pulse. Call
// once per emulator frame (50 Hz) after ExecuteFrame + Render.
// Currently only the ZX Printer uses it — for drum-timing math
// that needs to know "how many frames ago did the motor start".
func (pm *PeripheralManager) Frame() {
	if pm.zxprinter != nil {
		pm.zxprinter.Frame()
	}
}

// EnableZXPrinter attaches a ZX Printer to the peripheral bus.
// Prints accumulate in a bitmap buffer the caller can access via
// ZXPrinter() → Bitmap()/Save(). Idempotent.
func (pm *PeripheralManager) EnableZXPrinter() {
	if pm.zxprinterEnabled {
		return
	}
	pm.zxprinter = zxprinter.New()
	pm.zxprinterEnabled = true
	log.Println("ZX Printer enabled")
}

// DisableZXPrinter detaches the ZX Printer. Any accumulated print
// rows are dropped — callers who want to keep the bitmap should
// Save() first.
func (pm *PeripheralManager) DisableZXPrinter() {
	pm.zxprinterEnabled = false
	pm.zxprinter = nil
	log.Println("ZX Printer disabled")
}

// IsZXPrinterEnabled reports whether the printer is attached.
func (pm *PeripheralManager) IsZXPrinterEnabled() bool {
	return pm.zxprinterEnabled
}

// ZXPrinter returns the attached printer or nil. Used by the UI
// for Save / Clear menu actions.
func (pm *PeripheralManager) ZXPrinter() *zxprinter.Printer {
	return pm.zxprinter
}

// EnableKempstonMouse attaches a Kempston mouse to the peripheral
// bus. The mouse is idle until Move / SetButton calls arrive from
// the host event loop. Idempotent — calling again is a no-op.
func (pm *PeripheralManager) EnableKempstonMouse() {
	if pm.kempmouseEnabled {
		return
	}
	pm.kempmouse = kempmouse.New()
	pm.kempmouseEnabled = true
	log.Println("Kempston mouse enabled")
}

// DisableKempstonMouse detaches the Kempston mouse.
func (pm *PeripheralManager) DisableKempstonMouse() {
	pm.kempmouseEnabled = false
	pm.kempmouse = nil
	log.Println("Kempston mouse disabled")
}

// IsKempstonMouseEnabled reports whether the Kempston mouse is
// currently attached.
func (pm *PeripheralManager) IsKempstonMouseEnabled() bool {
	return pm.kempmouseEnabled
}

// KempstonMouseMove accumulates host mouse deltas into the
// Kempston X / Y counters. No-op if the mouse isn't enabled.
func (pm *PeripheralManager) KempstonMouseMove(dx, dy int) {
	if pm.kempmouse != nil {
		pm.kempmouse.Move(dx, dy)
	}
}

// KempstonMouseButton presses or releases a Kempston mouse button.
// btn is the button index (0 = right, 1 = left, per FUSE convention).
func (pm *PeripheralManager) KempstonMouseButton(btn int, pressed bool) {
	if pm.kempmouse != nil {
		pm.kempmouse.SetButton(btn, pressed)
	}
}

// IF1 returns the active Interface 1 device, or nil if not enabled.
// Used by the UI for cartridge insertion / ejection / write-protect.
func (pm *PeripheralManager) IF1() *if1.IF1 {
	return pm.if1
}

// IsInterface1Enabled reports whether the Interface 1 is currently
// active.
func (pm *PeripheralManager) IsInterface1Enabled() bool {
	return pm.if1Enabled
}

// CanEnableInterface1 reports whether the current machine model can
// host an Interface 1. The IF1 was a 48K-only peripheral; the menu
// item should be disabled on other models to make this clear.
func (pm *PeripheralManager) CanEnableInterface1() bool {
	return pm.modelHasIF1()
}

// MicrodriveSlotCount returns the number of microdrive slots the
// Interface 1 hardware supports (8). Exposed via the peripheral
// manager so UI code can iterate the drives without importing
// pkg/if1 directly.
func (pm *PeripheralManager) MicrodriveSlotCount() int {
	return if1.NumDrives
}

// LoadMicrodrive parses a .mdr cartridge file from disk and inserts
// it into the IF1's drive `which` (0-based). Mirrors the shape of
// LoadPlus3Disk so the menu code can be a one-liner.
func (pm *PeripheralManager) LoadMicrodrive(which int, path string) error {
	if pm.if1 == nil {
		return fmt.Errorf("interface 1 is not enabled")
	}
	cart, err := microdrive.ReadFile(path)
	if err != nil {
		return err
	}
	pm.if1.ULA.Bus.Insert(which, cart)
	return nil
}

// SaveMicrodrive writes the cartridge currently in drive `which`
// back to disk as a .mdr file. Returns an error if the drive is
// empty or the IF1 isn't enabled.
func (pm *PeripheralManager) SaveMicrodrive(which int, path string) error {
	if pm.if1 == nil {
		return fmt.Errorf("interface 1 is not enabled")
	}
	cart := pm.if1.Cartridge(which)
	if cart == nil {
		return fmt.Errorf("microdrive %d has no cartridge", which+1)
	}
	return cart.WriteFile(path)
}

// InsertMicrodrive places the supplied cartridge into the IF1's
// drive `which` (0-based). Used by tests and any caller that has
// already parsed a *microdrive.Cartridge in memory; the menu code
// uses LoadMicrodrive instead.
func (pm *PeripheralManager) InsertMicrodrive(which int, cart *microdrive.Cartridge) error {
	if pm.if1 == nil {
		return fmt.Errorf("interface 1 is not enabled")
	}
	pm.if1.ULA.Bus.Insert(which, cart)
	return nil
}

// EjectMicrodrive removes the cartridge from drive `which`.
func (pm *PeripheralManager) EjectMicrodrive(which int) {
	if pm.if1 == nil {
		return
	}
	pm.if1.ULA.Bus.Eject(which)
}

// SetMicrodriveWriteProtect toggles the write-protect flag on the
// cartridge currently in drive `which`.
func (pm *PeripheralManager) SetMicrodriveWriteProtect(which int, wp bool) {
	if pm.if1 == nil {
		return
	}
	cart := pm.if1.Cartridge(which)
	if cart == nil {
		return
	}
	cart.SetWriteProtect(wp)
}

// MicrodriveWriteProtected reports whether the cartridge in drive
// `which` is currently write-protected. Returns false if the slot
// is empty or the IF1 isn't enabled.
func (pm *PeripheralManager) MicrodriveWriteProtected(which int) bool {
	if pm.if1 == nil {
		return false
	}
	cart := pm.if1.Cartridge(which)
	if cart == nil {
		return false
	}
	return cart.WriteProtect()
}

// MicrodriveCartridgeInserted reports whether drive `which` has a
// cartridge. Used by the UI to grey out menu items when there's
// nothing to save / eject / write-protect.
func (pm *PeripheralManager) MicrodriveCartridgeInserted(which int) bool {
	if pm.if1 == nil {
		return false
	}
	return pm.if1.Cartridge(which) != nil
}

// IF1MemoryRead is the memory.PeripheralRead-compatible callback the
// emulator wires into the memory package so the IF1 shadow ROM can
// overlay the Spectrum ROM when paged in. Returns (0, false) when
// the IF1 isn't installed or isn't paged in — the memory package
// then falls through to the host ROM.
func (pm *PeripheralManager) IF1MemoryRead(addr uint16) (byte, bool) {
	if pm.if1 == nil {
		return 0, false
	}
	return pm.if1.MemoryRead(addr)
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

	// Interface 1 (48K only — see modelHasIF1).
	if pm.if1Enabled && pm.if1 != nil {
		if value, handled := pm.if1.HandlePortRead(port); handled {
			return value, true
		}
	}

	// Kempston mouse (any model).
	if pm.kempmouseEnabled && pm.kempmouse != nil {
		if value, handled := pm.kempmouse.HandlePortRead(port); handled {
			return value, true
		}
	}

	// ZX Printer — timing-sensitive, needs current T-states.
	if pm.zxprinterEnabled && pm.zxprinter != nil {
		if value, handled := pm.zxprinter.HandlePortRead(port, pm.currentTstates()); handled {
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

	// Interface 1 writes (48K only).
	if pm.if1Enabled && pm.if1 != nil {
		if pm.if1.HandlePortWrite(port, value) {
			handled = true
		}
	}

	// ZX Printer writes — timing-sensitive, needs current T-states.
	if pm.zxprinterEnabled && pm.zxprinter != nil {
		if pm.zxprinter.HandlePortWrite(port, value, pm.currentTstates()) {
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

	// Check if the Interface 1 shadow ROM is paged in. The 8 KB
	// IF1 ROM is mirrored across the full 0x0000-0x3FFF window —
	// see if1.IF1.MemoryRead.
	if pm.if1Enabled && pm.if1 != nil {
		if val, ok := pm.if1.MemoryRead(addr); ok {
			return val, true
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