package plus3fdc

// Raw-sector disk formats: MGT, IMG, SAD (DISCiPLE / +D / SAM Coupe)
// and TRD (TR-DOS / Beta). These formats contain no metadata — just
// packed sector data — so the geometry is either fixed (TRD: always
// 16×256 on 80 cylinders × 2 sides) or inferred from the file size
// (MGT/IMG: guess from the total byte count).
//
// Our internal Disk representation is always a spinning-disk byte
// stream, so the loaders use buildRawSectorTrack to wrap the sector
// bytes in a standard IBM34 MFM layout. That lets the same µPD765
// read/write path handle TR-DOS and DISCiPLE images transparently,
// even though they were originally intended for the Beta / WD1770
// disk interfaces.

import (
	"bytes"
	"fmt"
)

// buildRawSectorTrack wraps a flat block of sector bytes in a track
// byte stream using the supplied gap layout. Sector IDs start at
// baseSectorID and count upward (1..N by default).
func buildRawSectorTrack(data []byte, cyl, head byte, numSectors int, sectorSize int, baseSectorID byte, gap gapKind) (*Track, error) {
	sizeCode, ok := sectorSizeCode(sectorSize)
	if !ok {
		return nil, fmt.Errorf("unsupported sector size %d", sectorSize)
	}
	tr := newTrack(bytesPerTrackDD, 0xE5, cyl, head, sizeCode)
	b := newTrackBuilder(tr, gap)
	if !b.preindexAdd() {
		return nil, fmt.Errorf("track too small for pre-index gap")
	}
	if !b.postindexAdd() {
		return nil, fmt.Errorf("track too small for post-index gap")
	}
	for j := 0; j < numSectors; j++ {
		r := baseSectorID + byte(j)
		if !b.idAdd(cyl, head, r, sizeCode, false) {
			return nil, fmt.Errorf("track too small for sector %d ID", j)
		}
		sectorData := data[j*sectorSize : (j+1)*sectorSize]
		if _, ok := b.dataAdd(sectorData, false, false); !ok {
			return nil, fmt.Errorf("track too small for sector %d data", j)
		}
	}
	b.gap4Add()
	return tr, nil
}

// sectorSizeCode returns the µPD765 N value for the given byte length
// (128 << N). Supports the standard sizes 128, 256, 512, 1024, 2048,
// 4096, 8192.
func sectorSizeCode(size int) (byte, bool) {
	for n := 0; n <= 6; n++ {
		if size == 128<<n {
			return byte(n), true
		}
	}
	return 0, false
}

// ----------------------------------------------------------------------
// MGT / IMG (DISCiPLE / +D)
// ----------------------------------------------------------------------

// ParseMGT parses an in-memory MGT image. MGT/IMG files carry no
// geometry header, so the geometry is inferred from the file size
// against the known DISCiPLE/+D disk sizes (see guessMGTGeometry).
// MGT stores tracks in "side-alternating" order — cyl 0 head 0,
// cyl 0 head 1, cyl 1 head 0, ...
func ParseMGT(data []byte) (*Disk, error) {
	return parseRawSideAlternating(data, true)
}

// ParseIMG parses an in-memory IMG image. Same geometry as MGT but
// sectors are stored in "out-out" order — all of side 0's tracks, then
// all of side 1's.
func ParseIMG(data []byte) (*Disk, error) {
	return parseRawSideAlternating(data, false)
}

// parseRawSideAlternating is the shared MGT/IMG loader. altSides=true
// means side-alternating (MGT); altSides=false means out-out (IMG).
func parseRawSideAlternating(data []byte, altSides bool) (*Disk, error) {
	sides, cylinders, sectorsPerTrack, sectorSize, err := guessMGTGeometry(len(data))
	if err != nil {
		return nil, err
	}
	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}
	trackSize := sectorsPerTrack * sectorSize

	for logical := 0; logical < sides*cylinders; logical++ {
		var cyl, head int
		if altSides {
			cyl = logical / sides
			head = logical % sides
		} else {
			cyl = logical % cylinders
			head = logical / cylinders
		}
		offset := logical * trackSize
		trackData := data[offset : offset+trackSize]
		tr, err := buildRawSectorTrack(trackData, byte(cyl), byte(head),
			sectorsPerTrack, sectorSize, 1, gapMGT)
		if err != nil {
			return nil, fmt.Errorf("track c=%d h=%d: %w", cyl, head, err)
		}
		d.Tracks[head][cyl] = tr
	}
	return d, nil
}

// guessMGTGeometry returns (sides, cylinders, sectorsPerTrack,
// sectorSize) for a given MGT/IMG file size, matched against the
// standard DISCiPLE/+D disk sizes (single/double-sided, 40/80 track,
// 10×512 or 18×256 byte sectors).
func guessMGTGeometry(size int) (sides, cyls, sectors, seclen int, err error) {
	switch size {
	case 2 * 80 * 10 * 512:
		return 2, 80, 10, 512, nil
	case 1 * 80 * 10 * 512:
		// Could also be 2*40*10*512; prefer the 80-cyl interpretation.
		return 1, 80, 10, 512, nil
	case 1 * 40 * 10 * 512:
		return 1, 40, 10, 512, nil
	case 2 * 80 * 18 * 256:
		return 2, 80, 18, 256, nil
	case 1 * 80 * 18 * 256:
		return 1, 80, 18, 256, nil
	case 1 * 40 * 18 * 256:
		return 1, 40, 18, 256, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("MGT/IMG: can't guess geometry for %d bytes", size)
}

// ----------------------------------------------------------------------
// TRD (TR-DOS / Beta)
// ----------------------------------------------------------------------

// ----------------------------------------------------------------------
// SAD (DISCiPLE / +D with an explicit geometry header)
// ----------------------------------------------------------------------

var sadSignature = []byte("Aley's disk backup")

// ParseSAD parses an in-memory SAD image. SAD is a DISCiPLE / +D
// variant with an 18-byte "Aley's disk backup" magic followed by a
// 4-byte geometry header: sides, cylinders, sectors/track, (sector
// size / 64). Sectors are stored in out-out order.
func ParseSAD(data []byte) (*Disk, error) {
	if len(data) < 22 {
		return nil, fmt.Errorf("SAD too short: %d bytes", len(data))
	}
	if !bytes.HasPrefix(data, sadSignature) {
		return nil, fmt.Errorf("not a SAD image: signature %q", data[:len(sadSignature)])
	}
	sides := int(data[18])
	cylinders := int(data[19])
	sectors := int(data[20])
	sectorSize := int(data[21]) * 64
	if sides < 1 || sides > 2 {
		return nil, fmt.Errorf("SAD: invalid sides %d", sides)
	}
	if cylinders < 1 || cylinders > 84 {
		return nil, fmt.Errorf("SAD: invalid cylinders %d", cylinders)
	}
	if _, ok := sectorSizeCode(sectorSize); !ok {
		return nil, fmt.Errorf("SAD: unsupported sector size %d", sectorSize)
	}
	trackSize := sectors * sectorSize
	need := 22 + sides*cylinders*trackSize
	if len(data) < need {
		return nil, fmt.Errorf("SAD: file too short (%d, need %d)", len(data), need)
	}

	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}

	// SAD stores sectors in out-out order: all of side 0, then all of side 1.
	offset := 22
	for h := 0; h < sides; h++ {
		for c := 0; c < cylinders; c++ {
			trackData := data[offset : offset+trackSize]
			tr, err := buildRawSectorTrack(trackData, byte(c), byte(h),
				sectors, sectorSize, 1, gapMGT)
			if err != nil {
				return nil, fmt.Errorf("SAD track c=%d h=%d: %w", c, h, err)
			}
			d.Tracks[h][c] = tr
			offset += trackSize
		}
	}
	return d, nil
}

// ----------------------------------------------------------------------
// D40 / D80 (Didaktik 40 / 80)
// ----------------------------------------------------------------------

// ParseD40D80 parses an in-memory Didaktik 40 / 80 image. The geometry
// lives in the first track's boot sector at fixed offsets (bytes
// 0xB1..0xB3), and all sectors are 512 bytes. Stored in out-out order.
func ParseD40D80(data []byte) (*Disk, error) {
	if len(data) < 0xB4 {
		return nil, fmt.Errorf("D40/D80 too short: %d bytes", len(data))
	}
	sides := 1
	if data[0xB1]&0x10 != 0 {
		sides = 2
	}
	cylinders := int(data[0xB2])
	sectors := int(data[0xB3])
	if sides < 1 || sides > 2 || cylinders == 0 || cylinders > 83 || sectors == 0 || sectors > 127 {
		return nil, fmt.Errorf("D40/D80: invalid geometry sides=%d cyls=%d sectors=%d",
			sides, cylinders, sectors)
	}
	const sectorSize = 512
	trackSize := sectors * sectorSize
	need := sides * cylinders * trackSize
	if len(data) < need {
		return nil, fmt.Errorf("D40/D80: file too short (%d, need %d)", len(data), need)
	}

	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}

	// Didaktik stores tracks in (cylinder, head) order, like MGT.
	offset := 0
	for c := 0; c < cylinders; c++ {
		for h := 0; h < sides; h++ {
			trackData := data[offset : offset+trackSize]
			tr, err := buildRawSectorTrack(trackData, byte(c), byte(h),
				sectors, sectorSize, 1, gapMGT)
			if err != nil {
				return nil, fmt.Errorf("D40/D80 track c=%d h=%d: %w", c, h, err)
			}
			d.Tracks[h][c] = tr
			offset += trackSize
		}
	}
	return d, nil
}

// ParseTRD parses an in-memory TR-DOS TRD image. TR-DOS disks are
// always formatted as 2 sides × 80 cylinders × 16 sectors × 256
// bytes (655360 bytes total), though partial images with fewer tracks
// are accepted. Sectors are stored in side-alternating order starting
// at sector ID 1.
func ParseTRD(data []byte) (*Disk, error) {
	const (
		sectorsPerTrack = 16
		sectorSize      = 256
		sides           = 2
		maxCylinders    = 80
	)
	trackSize := sectorsPerTrack * sectorSize
	if len(data) == 0 || len(data)%trackSize != 0 {
		return nil, fmt.Errorf("TRD: length %d not a multiple of %d", len(data), trackSize)
	}
	numTracks := len(data) / trackSize
	if numTracks > sides*maxCylinders {
		return nil, fmt.Errorf("TRD: too many tracks (%d)", numTracks)
	}
	cylinders := (numTracks + sides - 1) / sides
	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}
	for logical := 0; logical < numTracks; logical++ {
		cyl := logical / sides
		head := logical % sides
		offset := logical * trackSize
		trackData := data[offset : offset+trackSize]
		tr, err := buildRawSectorTrack(trackData, byte(cyl), byte(head),
			sectorsPerTrack, sectorSize, 1, gapTRDOS)
		if err != nil {
			return nil, fmt.Errorf("track c=%d h=%d: %w", cyl, head, err)
		}
		d.Tracks[head][cyl] = tr
	}
	return d, nil
}
