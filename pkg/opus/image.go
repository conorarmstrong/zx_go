package opus

import "fmt"

// Opus Discovery disk geometry. A disk is 40 cylinders of 18 256-byte
// sectors on a single side, stored as a flat image with no header — 184320
// bytes. Confirmed against three unrelated real .OPD images, each exactly
// that size, and against the 180K figure the interface is documented with.
const (
	Cylinders       = 40
	SectorsPerTrack = 18
	SectorSize      = 256
	TrackBytes      = SectorsPerTrack * SectorSize
	ImageBytes      = Cylinders * TrackBytes
)

// Image is one Opus disk.
type Image struct {
	Data []byte
}

// NewImage returns a blank formatted-size image.
func NewImage() *Image { return &Image{Data: make([]byte, ImageBytes)} }

// LoadImage adopts a raw .OPD image. The size is the only validation
// available: the format carries no header or signature.
func LoadImage(data []byte) (*Image, error) {
	if len(data) != ImageBytes {
		return nil, fmt.Errorf("opus: image is %d bytes, want %d (40 x 18 x 256)",
			len(data), ImageBytes)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return &Image{Data: cp}, nil
}

// offset resolves a track and sector to a byte offset. Both are 0-based:
// the Opus numbers its sectors from zero, which is not the usual Western
// Digital convention but is what the ROM asks for. Its drive table records
// "(03) no extra sectors" (DEFB +00), and RD_WR_M_9 writes the sector number
// to $2802 with only that offset added, so the very first block of the disk
// is requested as track 0, sector 0.
func (img *Image) offset(track, sector int) (int, error) {
	if track < 0 || track >= Cylinders {
		return 0, fmt.Errorf("opus: track %d out of range 0..%d", track, Cylinders-1)
	}
	if sector < 0 || sector >= SectorsPerTrack {
		return 0, fmt.Errorf("opus: sector %d out of range 0..%d", sector, SectorsPerTrack-1)
	}
	return track*TrackBytes + sector*SectorSize, nil
}

// ReadSector returns a copy of one sector.
func (img *Image) ReadSector(track, sector int) ([]byte, error) {
	off, err := img.offset(track, sector)
	if err != nil {
		return nil, err
	}
	out := make([]byte, SectorSize)
	copy(out, img.Data[off:off+SectorSize])
	return out, nil
}

// WriteSector replaces one sector. Short buffers are zero-padded.
func (img *Image) WriteSector(track, sector int, buf []byte) error {
	off, err := img.offset(track, sector)
	if err != nil {
		return err
	}
	for i := 0; i < SectorSize; i++ {
		var b byte
		if i < len(buf) {
			b = buf[i]
		}
		img.Data[off+i] = b
	}
	return nil
}
