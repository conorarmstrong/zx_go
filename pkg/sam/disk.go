package sam

import "fmt"

// Disk is a SAM Coupé floppy image: a flat sector store plus its geometry.
// The standard SAM disk is 800K MGT (80 cylinders × 2 heads × 10 sectors × 512
// bytes); SAD images carry their own geometry header.
type Disk struct {
	data            []byte
	cyls            int
	heads           int
	sectorsPerTrack int
	sectorSize      int
	headMajor       bool // SAD lays tracks out head-major; MGT is cylinder-major
	writeProtect    bool
}

const (
	mgtSectorSize  = 512
	mgtCyls        = 80
	mgtHeads       = 2
	mgt800KSectors = 10 // 80×2×10×512 = 819200 (SAMDOS/MasterDOS)
	mgt720KSectors = 9  // 80×2×9×512  = 737280 (DOS/+D)
	mgt800KSize    = mgtCyls * mgtHeads * mgt800KSectors * mgtSectorSize
	mgt720KSize    = mgtCyls * mgtHeads * mgt720KSectors * mgtSectorSize
	sadSignature   = "Aley's disk backup"
	sadHeaderLen   = 22 // 18-byte signature + heads + cyls + sectors + size/64
	sadSizeDivisor = 64
)

// Geometry reports the disk's cylinders, heads, sectors-per-track and sector
// size.
func (d *Disk) Geometry() (cyls, heads, sectorsPerTrack, sectorSize int) {
	return d.cyls, d.heads, d.sectorsPerTrack, d.sectorSize
}

// WriteProtected reports the write-protect state.
func (d *Disk) WriteProtected() bool { return d.writeProtect }

// SetWriteProtect sets the write-protect flag.
func (d *Disk) SetWriteProtect(wp bool) { d.writeProtect = wp }

// offset returns the byte offset of (cyl, head, sector) — sector is 1-based —
// and whether it is in range.
func (d *Disk) offset(cyl, head, sector int) (int, bool) {
	if cyl < 0 || cyl >= d.cyls || head < 0 || head >= d.heads ||
		sector < 1 || sector > d.sectorsPerTrack {
		return 0, false
	}
	var track int
	if d.headMajor {
		track = head*d.cyls + cyl
	} else {
		track = cyl*d.heads + head
	}
	off := (track*d.sectorsPerTrack + (sector - 1)) * d.sectorSize
	if off+d.sectorSize > len(d.data) {
		return 0, false
	}
	return off, true
}

// ReadSector returns a copy of the sector at (cyl, head, sector), or false if
// the address is out of range.
func (d *Disk) ReadSector(cyl, head, sector int) ([]byte, bool) {
	off, ok := d.offset(cyl, head, sector)
	if !ok {
		return nil, false
	}
	out := make([]byte, d.sectorSize)
	copy(out, d.data[off:off+d.sectorSize])
	return out, true
}

// WriteSector overwrites the sector at (cyl, head, sector); fails if out of
// range or write-protected.
func (d *Disk) WriteSector(cyl, head, sector int, buf []byte) bool {
	if d.writeProtect {
		return false
	}
	off, ok := d.offset(cyl, head, sector)
	if !ok {
		return false
	}
	copy(d.data[off:off+d.sectorSize], buf)
	return true
}

// LoadDisk parses a SAM disk image, detecting MGT (by size) or SAD (by header
// signature). EDSK / SBT are not yet supported.
func LoadDisk(data []byte) (*Disk, error) {
	if len(data) >= len(sadSignature) && string(data[:len(sadSignature)]) == sadSignature {
		return loadSAD(data)
	}
	switch len(data) {
	case mgt800KSize:
		return loadMGT(data, mgt800KSectors), nil
	case mgt720KSize:
		return loadMGT(data, mgt720KSectors), nil
	}
	return nil, fmt.Errorf("sam: unrecognised disk image (%d bytes; not 800K/720K MGT or SAD)", len(data))
}

func loadMGT(data []byte, sectors int) *Disk {
	d := &Disk{
		cyls:            mgtCyls,
		heads:           mgtHeads,
		sectorsPerTrack: sectors,
		sectorSize:      mgtSectorSize,
		headMajor:       false,
		data:            make([]byte, len(data)),
	}
	copy(d.data, data)
	return d
}

func loadSAD(data []byte) (*Disk, error) {
	if len(data) < sadHeaderLen {
		return nil, fmt.Errorf("sam: truncated SAD header")
	}
	heads := int(data[18])
	cyls := int(data[19])
	sectors := int(data[20])
	sectorSize := int(data[21]) * sadSizeDivisor
	if heads < 1 || heads > 2 || cyls < 1 || cyls > 83 || sectors < 1 || sectorSize < 128 {
		return nil, fmt.Errorf("sam: invalid SAD geometry %dx%dx%d size %d", cyls, heads, sectors, sectorSize)
	}
	d := &Disk{
		cyls:            cyls,
		heads:           heads,
		sectorsPerTrack: sectors,
		sectorSize:      sectorSize,
		headMajor:       true,
		data:            make([]byte, len(data)-sadHeaderLen),
	}
	copy(d.data, data[sadHeaderLen:])
	return d, nil
}

// blankMGT builds an empty 800K MGT disk (used by tests and formatting).
func blankMGT() *Disk {
	return loadMGT(make([]byte, mgt800KSize), mgt800KSectors)
}
