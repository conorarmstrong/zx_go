// Package betadisk implements the Beta Disk interface and its WD1793
// floppy-disk controller — the de-facto disk standard for the ZX Spectrum 128
// clones (Pentagon, Scorpion) running TR-DOS. It provides:
//
//   - Image: a raw TR-DOS .TRD sector image with (cylinder, side, sector)
//     addressing.
//   - FDC: a WD1793 controller modelling the command/status/track/sector/data
//     registers and the seek/read/write/read-address commands TR-DOS issues.
//   - Interface: the Beta port decode ($1F/$3F/$5F/$7F FDC registers + $FF
//     system register) and the drive/side selection that wires the FDC to a
//     mounted image.
//
// The TR-DOS ROM auto-paging (the Beta hardware swaps its ROM in when the CPU
// fetches from $3Dxx and out at $4000+) lives in pkg/memory, driven by a CPU
// pre-fetch hook; this package is hardware-state only and has no CPU or memory
// dependencies, so it is exhaustively unit-testable in isolation.
package betadisk

import "fmt"

// TR-DOS fixed geometry: a sector is always 256 bytes and a track always holds
// 16 sectors numbered 1..16.
const (
	SectorSize      = 256
	SectorsPerTrack = 16
	trackBytes      = SectorsPerTrack * SectorSize
)

// Image is a raw TR-DOS disk image: a flat byte slice addressed by
// (cylinder, side, sector). The on-disk .TRD layout is side-interleaved within
// a cylinder — logical track = cylinder*sides + side — with each track holding
// 16 contiguous 256-byte sectors.
type Image struct {
	Data      []byte
	Cylinders int
	Sides     int
}

// NewImage returns a zero-filled image of the given geometry.
func NewImage(cylinders, sides int) *Image {
	return &Image{
		Data:      make([]byte, cylinders*sides*trackBytes),
		Cylinders: cylinders,
		Sides:     sides,
	}
}

// diskTypeOffset is the byte in track 0, sector 9 (1-based) that records the
// TR-DOS disk geometry: 0x16=80 DS, 0x17=40 DS, 0x18=80 SS, 0x19=40 SS.
const diskTypeOffset = 0x8E3

// LoadImage wraps a raw .TRD dump, inferring 80/40-track and single/double-sided
// geometry from its length. The 327680-byte size is ambiguous (40-track DS and
// 80-track SS are the same length); the disk-type byte at 0x8E3 disambiguates,
// defaulting to the more common 40-track DS when blank. Sizes that don't match
// a standard TR-DOS disk are rejected.
func LoadImage(data []byte) (*Image, error) {
	switch len(data) {
	case 80 * 2 * trackBytes:
		return &Image{Data: data, Cylinders: 80, Sides: 2}, nil
	case 40 * 1 * trackBytes:
		return &Image{Data: data, Cylinders: 40, Sides: 1}, nil
	case 40 * 2 * trackBytes: // == 80*1*trackBytes — ambiguous
		if len(data) > diskTypeOffset && data[diskTypeOffset] == 0x18 {
			return &Image{Data: data, Cylinders: 80, Sides: 1}, nil
		}
		return &Image{Data: data, Cylinders: 40, Sides: 2}, nil
	}
	return nil, fmt.Errorf("not a TR-DOS .TRD image: %d bytes is not a standard disk size", len(data))
}

// offset returns the byte offset of (cylinder, side, sector) within Data, or an
// error if any coordinate is outside the image geometry. sector is 1-based.
func (img *Image) offset(cylinder, side, sector int) (int, error) {
	if cylinder < 0 || cylinder >= img.Cylinders {
		return 0, fmt.Errorf("cylinder %d out of range 0..%d", cylinder, img.Cylinders-1)
	}
	if side < 0 || side >= img.Sides {
		return 0, fmt.Errorf("side %d out of range 0..%d", side, img.Sides-1)
	}
	if sector < 1 || sector > SectorsPerTrack {
		return 0, fmt.Errorf("sector %d out of range 1..%d", sector, SectorsPerTrack)
	}
	logicalTrack := cylinder*img.Sides + side
	return logicalTrack*trackBytes + (sector-1)*SectorSize, nil
}

// ReadSector returns a copy of the 256-byte sector at (cylinder, side, sector).
func (img *Image) ReadSector(cylinder, side, sector int) ([]byte, error) {
	off, err := img.offset(cylinder, side, sector)
	if err != nil {
		return nil, err
	}
	out := make([]byte, SectorSize)
	copy(out, img.Data[off:off+SectorSize])
	return out, nil
}

// WriteSector overwrites the 256-byte sector at (cylinder, side, sector). buf
// must be exactly one sector.
func (img *Image) WriteSector(cylinder, side, sector int, buf []byte) error {
	if len(buf) != SectorSize {
		return fmt.Errorf("sector buffer is %d bytes, want %d", len(buf), SectorSize)
	}
	off, err := img.offset(cylinder, side, sector)
	if err != nil {
		return err
	}
	copy(img.Data[off:off+SectorSize], buf)
	return nil
}
