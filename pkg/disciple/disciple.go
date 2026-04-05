package disciple

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// Disciple represents the DISCiPLE disk interface by Miles Gordon Technology
type Disciple struct {
	// ROM data (8KB GDOS)
	rom []byte
	
	// RAM data (8KB)
	ram []byte
	
	// Control registers
	control   byte
	status    byte
	track     byte
	sector    byte
	data      byte
	
	// Drive status
	drive       byte
	currentDisk string
	
	// Interface status
	enabled    bool
	inhibited  bool
	romPaged   bool
	
	// NMI button state
	nmiPressed bool
	
	// Memory reference for paging
	memory *memory.Memory
}

// Port addresses for Disciple interface
const (
	PortControl = 0x1F   // Control register
	PortStatus  = 0x2F   // Status register  
	PortTrack   = 0x3F   // Track register
	PortSector  = 0x4F   // Sector register
	PortData    = 0x5F   // Data register
	PortDrive   = 0x6F   // Drive select
	PortNMI     = 0x7F   // NMI control
	PortSystem  = 0x8F   // System control
)

// Commands for the disk controller
const (
	CmdRestore      = 0x00
	CmdSeek         = 0x10
	CmdStep         = 0x20
	CmdStepIn       = 0x40
	CmdStepOut      = 0x60
	CmdReadSector   = 0x80
	CmdWriteSector  = 0xA0
	CmdReadAddress  = 0xC0
	CmdForceInt     = 0xD0
	CmdReadTrack    = 0xE0
	CmdWriteTrack   = 0xF0
)

// NewDisciple creates a new Disciple interface
func NewDisciple(romPath string, memory *memory.Memory) (*Disciple, error) {
	d := &Disciple{
		ram:    make([]byte, 0x2000), // 8KB RAM
		memory: memory,
	}
	
	// Load GDOS ROM
	if err := d.loadROM(romPath); err != nil {
		return nil, fmt.Errorf("failed to load Disciple ROM: %w", err)
	}
	
	d.reset()
	return d, nil
}

// loadROM loads the GDOS ROM
func (d *Disciple) loadROM(romPath string) error {
	// Try different possible ROM filenames
	romFiles := []string{"gdos.rom", "disciple.rom", "GDOS_3d.rom"}
	
	for _, filename := range romFiles {
		path := filepath.Join(romPath, filename)
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			
			// GDOS ROM should be 8KB
			if len(data) == 0x2000 {
				d.rom = data
				return nil
			}
		}
	}
	
	// Create a minimal placeholder ROM if no real ROM found
	d.rom = make([]byte, 0x2000)
	// Add basic GDOS signature
	copy(d.rom[0:], []byte{0xF3, 0xC3, 0x00, 0x20}) // DI, JP 0x2000
	fmt.Println("Warning: Using placeholder GDOS ROM - disk functionality will be limited")
	
	return nil
}

// Reset resets the Disciple interface to initial state
func (d *Disciple) reset() {
	d.control = 0
	d.status = 0
	d.track = 0
	d.sector = 1
	d.data = 0
	d.drive = 0
	d.enabled = true
	d.inhibited = false
	d.romPaged = false
	d.nmiPressed = false
}

// HandlePortRead handles reads from Disciple I/O ports
func (d *Disciple) HandlePortRead(port uint16) (byte, bool) {
	if d.inhibited || !d.enabled {
		return 0, false
	}
	
	switch port & 0xFF {
	case PortControl:
		return d.control, true
	case PortStatus:
		return d.getStatus(), true
	case PortTrack:
		return d.track, true
	case PortSector:
		return d.sector, true
	case PortData:
		return d.data, true
	case PortDrive:
		return d.drive, true
	case PortSystem:
		return d.getSystemStatus(), true
	}
	
	return 0, false
}

// HandlePortWrite handles writes to Disciple I/O ports
func (d *Disciple) HandlePortWrite(port uint16, value byte) bool {
	if d.inhibited || !d.enabled {
		return false
	}
	
	switch port & 0xFF {
	case PortControl:
		d.executeCommand(value)
		return true
	case PortTrack:
		d.track = value
		return true
	case PortSector:
		d.sector = value
		return true
	case PortData:
		d.data = value
		return true
	case PortDrive:
		d.selectDrive(value)
		return true
	case PortNMI:
		d.handleNMIControl(value)
		return true
	case PortSystem:
		d.handleSystemControl(value)
		return true
	}
	
	return false
}

// getStatus returns the current status register
func (d *Disciple) getStatus() byte {
	status := byte(0)
	
	// Bit 0: Not ready (drive not ready)
	if d.currentDisk == "" {
		status |= 0x01
	}
	
	// Bit 1: Write protect (assume not protected)
	// status |= 0x02
	
	// Bit 2: Head loaded (assume loaded if disk present)
	if d.currentDisk != "" {
		status |= 0x04
	}
	
	// Bit 3: Seek error (assume no error)
	// status |= 0x08
	
	// Bit 4: CRC error (assume no error)
	// status |= 0x10
	
	// Bit 5: Lost data (assume no error)
	// status |= 0x20
	
	// Bit 6: Data request (assume ready)
	status |= 0x40
	
	// Bit 7: Not busy (assume ready)
	status |= 0x80
	
	return status
}

// getSystemStatus returns system control status
func (d *Disciple) getSystemStatus() byte {
	status := byte(0)
	
	if d.romPaged {
		status |= 0x01
	}
	
	if d.nmiPressed {
		status |= 0x02
	}
	
	return status
}

// executeCommand executes a disk controller command
func (d *Disciple) executeCommand(cmd byte) {
	d.control = cmd
	
	cmdType := cmd & 0xF0
	
	switch cmdType {
	case CmdRestore:
		d.track = 0
		d.status = 0x80 // Not busy
		
	case CmdSeek:
		// Seek to track in data register
		d.track = d.data
		d.status = 0x80 // Not busy
		
	case CmdStep:
		d.status = 0x80 // Not busy

	case CmdStepIn:
		if d.track < 79 {
			d.track++
		}
		d.status = 0x80 // Not busy

	case CmdStepOut:
		if d.track > 0 {
			d.track--
		}
		d.status = 0x80 // Not busy
		
	case CmdReadSector:
		d.readSector()
		
	case CmdWriteSector:
		d.writeSector()
		
	case CmdForceInt:
		// Force interrupt - clear status
		d.status = 0x80
		
	default:
		// Unknown command
		d.status = 0x80
	}
}

// readSector reads a sector from disk
func (d *Disciple) readSector() {
	// Placeholder implementation
	// In a real implementation, this would read from an MGT disk image
	d.data = 0x00
	d.status = 0x80 // Not busy
}

// writeSector writes a sector to disk
func (d *Disciple) writeSector() {
	// Placeholder implementation
	// In a real implementation, this would write to an MGT disk image
	d.status = 0x80 // Not busy
}

// selectDrive selects the active drive
func (d *Disciple) selectDrive(value byte) {
	d.drive = value & 0x03 // Only 2 drives supported
}

// handleNMIControl handles NMI button control
func (d *Disciple) handleNMIControl(value byte) {
	if value&0x01 != 0 {
		d.nmiPressed = true
		d.pageInROM()
	} else {
		d.nmiPressed = false
		d.pageOutROM()
	}
}

// handleSystemControl handles system control commands
func (d *Disciple) handleSystemControl(value byte) {
	// Bit 0: ROM page control
	if value&0x01 != 0 {
		d.pageInROM()
	} else {
		d.pageOutROM()
	}
	
	// Bit 1: Inhibit control
	if value&0x02 != 0 {
		d.inhibited = true
		d.pageOutROM()
	} else {
		d.inhibited = false
	}
}

// pageInROM pages in the Disciple ROM
func (d *Disciple) pageInROM() {
	if d.inhibited {
		return
	}
	
	// In a real implementation, this would modify the memory map
	// to page in the Disciple ROM at 0x0000-0x1FFF
	d.romPaged = true
}

// pageOutROM pages out the Disciple ROM
func (d *Disciple) pageOutROM() {
	d.romPaged = false
}

// IsEnabled returns whether the Disciple is enabled
func (d *Disciple) IsEnabled() bool {
	return d.enabled && !d.inhibited
}

// IsInhibited returns whether the Disciple is inhibited
func (d *Disciple) IsInhibited() bool {
	return d.inhibited
}

// IsROMPaged returns whether the Disciple ROM is paged in
func (d *Disciple) IsROMPaged() bool {
	return d.romPaged
}

// GetROM returns the Disciple ROM data
func (d *Disciple) GetROM() []byte {
	return d.rom
}

// GetRAM returns the Disciple RAM data
func (d *Disciple) GetRAM() []byte {
	return d.ram
}

// SetInhibit sets the inhibit state
func (d *Disciple) SetInhibit(inhibit bool) {
	d.inhibited = inhibit
	if inhibit {
		d.pageOutROM()
	}
}

// LoadDisk loads a disk image (placeholder)
func (d *Disciple) LoadDisk(drive int, filename string) error {
	if drive < 0 || drive > 1 {
		return fmt.Errorf("invalid drive: %d", drive)
	}
	
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return fmt.Errorf("disk image not found: %s", filename)
	}
	
	d.currentDisk = filename
	return nil
}