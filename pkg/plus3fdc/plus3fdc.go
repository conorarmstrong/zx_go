package plus3fdc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plus3FDC is the +3 / +2A floppy disk controller peripheral. It wraps a
// UPD765 instance and exposes the port-level interface that
// PeripheralManager talks to. The +3 decodes:
//
//	port & 0xF002 == 0x2000  →  FDC main status register (read)
//	port & 0xF002 == 0x3000  →  FDC data register (read/write)
type Plus3FDC struct {
	fdc *UPD765
}

// New returns a freshly-reset Plus3FDC with no disk loaded.
func New() *Plus3FDC {
	return &Plus3FDC{fdc: NewUPD765()}
}

// LoadDisk parses a disk image (DSK / EDSK / UDI / MGT / IMG / TRD)
// and attaches it to the given physical drive (0 = A, 1 = B).
// Replaces any previously-loaded disk in that slot. Format is
// auto-detected: signature first, then file extension for headerless
// formats like MGT and TRD.
func (p *Plus3FDC) LoadDisk(drive int, path string) error {
	d, err := loadDiskByPath(path)
	if err != nil {
		return fmt.Errorf("plus3fdc: %w", err)
	}
	p.fdc.AttachDisk(drive, d)
	return nil
}

// loadDiskByPath reads the file, tries signature-based detection
// first, then falls back to matching the file extension for raw
// headerless formats.
func loadDiskByPath(path string) (*Disk, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read disk image: %w", err)
	}
	// Signature-first dispatch.
	if d, err := ParseDiskImage(data); err == nil {
		return d, nil
	}
	// Extension fallback for raw-sector formats without magic bytes.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mgt":
		return ParseMGT(data)
	case ".img":
		return ParseIMG(data)
	case ".trd":
		return ParseTRD(data)
	case ".d40", ".d80":
		return ParseD40D80(data)
	}
	return nil, fmt.Errorf("unrecognised disk image format at %s", path)
}

// EjectDisk removes the disk in the given drive, if any.
func (p *Plus3FDC) EjectDisk(drive int) {
	p.fdc.AttachDisk(drive, nil)
}

// SaveDisk serialises the disk in the given drive back to a file on
// host disk. Fails if no disk is currently mounted.
func (p *Plus3FDC) SaveDisk(drive int, path string) error {
	data, err := p.fdc.serializeDisk(drive)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SetWriteProtect toggles the per-drive write-protect flag.
func (p *Plus3FDC) SetWriteProtect(drive int, wp bool) {
	p.fdc.SetWriteProtect(drive, wp)
}

// SetSpeedlockEnabled toggles the Speedlock copy-protection workaround.
func (p *Plus3FDC) SetSpeedlockEnabled(enabled bool) {
	p.fdc.SetSpeedlockEnabled(enabled)
}

// HandlePortRead returns (value, true) if the FDC handled the port; the
// PeripheralManager uses this to know whether to fall through to other
// peripherals. The +3 decodes:
//
//	port & 0xF002 == 0x2000  →  main status register (read only)
//	port & 0xF002 == 0x3000  →  data register (read/write)
func (p *Plus3FDC) HandlePortRead(port uint16) (byte, bool) {
	switch port & 0xF002 {
	case 0x2000:
		return p.fdc.ReadStatus(), true
	case 0x3000:
		return p.fdc.ReadData(), true
	}
	return 0, false
}

// HandlePortWrite forwards FDC data writes. Writes to the status port are
// undefined on the µPD765 (it's read-only) but we report the port as
// handled so the floating bus doesn't kick in.
func (p *Plus3FDC) HandlePortWrite(port uint16, val byte) bool {
	switch port & 0xF002 {
	case 0x3000:
		p.fdc.WriteData(val)
		return true
	case 0x2000:
		return true
	}
	return false
}
