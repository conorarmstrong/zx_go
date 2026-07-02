// Package plus3fdc implements the floppy disk controller for the Spectrum
// +3 / +2A: a NEC µPD765A FDC plus disk image parsers for DSK / EDSK,
// UDI, MGT / IMG, SAD, TRD, and D40 / D80. The internal disk
// representation is a track byte stream with parallel clock, FM, and
// weak-data bitmaps — see track.go for the model.
package plus3fdc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Sector is a logical view of a single sector recovered by walking a
// track's byte stream. The Data slice is fresh — it's NOT a slice
// into the underlying track storage — so callers can keep references
// across disk swaps. Used by test helpers only; the FDC walks tracks
// directly via Track.idAt / Track.dataAt.
type Sector struct {
	C    byte
	H    byte
	R    byte
	N    byte
	Data []byte
}

// FindSector locates a sector by R on the track by walking the byte
// stream for matching IDAMs. Returns nil if not present. Used by tests
// for verification; the production FDC walks track.idAt directly.
func (t *Track) FindSector(r byte) *Sector {
	if t == nil {
		return nil
	}
	pos := 0
	for {
		c, h, rid, n, idEnd, ok := t.idAt(pos)
		if !ok {
			return nil
		}
		if rid == r {
			ds, _, dok := t.dataAt(idEnd)
			if !dok {
				return nil
			}
			length := sectorLength(n)
			if ds+length > t.bpt {
				length = t.bpt - ds
			}
			data := make([]byte, length)
			for i := 0; i < length; i++ {
				data[i] = t.readByte(ds + i)
			}
			return &Sector{C: c, H: h, R: r, N: n, Data: data}
		}
		pos = idEnd
	}
}

// Disk is a parsed DSK disk image — a 2D grid of Tracks indexed by
// [head][cylinder]. Tracks may be nil for absent tracks in EDSK images.
type Disk struct {
	Sides     int
	Cylinders int
	Tracks    [][]*Track // [head][cylinder]; nil entry = no such track
}

// Track returns the track at the given head/cylinder, or nil if absent.
func (d *Disk) Track(head, cylinder int) *Track {
	if head < 0 || head >= len(d.Tracks) {
		return nil
	}
	if cylinder < 0 || cylinder >= len(d.Tracks[head]) {
		return nil
	}
	return d.Tracks[head][cylinder]
}

// FormatTrack rebuilds the track at (head, cylinder) from the given
// sectors, replacing whatever was there — the WD177x Write Track (format)
// operation. Each sector's C/H/R/N becomes its ID field and Data its
// formatted (filler) contents.
func (d *Disk) FormatTrack(head, cylinder int, sectors []Sector) error {
	if head < 0 || head >= len(d.Tracks) {
		return fmt.Errorf("plus3fdc: FormatTrack: head %d out of range", head)
	}
	if cylinder < 0 || cylinder >= len(d.Tracks[head]) {
		return fmt.Errorf("plus3fdc: FormatTrack: cylinder %d out of range", cylinder)
	}
	var c, h, n byte
	if len(sectors) > 0 {
		c, h, n = sectors[0].C, sectors[0].H, sectors[0].N
	}
	tr := newTrack(bytesPerTrackDD, gapByteMFM, c, h, n)
	b := newTrackBuilder(tr, gapMFM)
	if !b.preindexAdd() {
		return fmt.Errorf("plus3fdc: FormatTrack: track too small for pre-index gap")
	}
	if !b.postindexAdd() {
		return fmt.Errorf("plus3fdc: FormatTrack: track too small for post-index gap")
	}
	for j, s := range sectors {
		if !b.idAdd(s.C, s.H, s.R, s.N, false) {
			return fmt.Errorf("plus3fdc: FormatTrack: track full at sector %d of %d", j, len(sectors))
		}
		if _, ok := b.dataAdd(s.Data, false, false); !ok {
			return fmt.Errorf("plus3fdc: FormatTrack: track full writing data for sector %d of %d", j, len(sectors))
		}
	}
	b.gap4Add()
	d.Tracks[head][cylinder] = tr
	return nil
}

const (
	dskHeaderSize    = 256
	trackHeaderSize  = 256
	sectorInfoSize   = 8
	sectorInfoOffset = 0x18
)

var (
	dskSignatureStd    = []byte("MV - CPCEMU Disk-File")
	dskSignatureExt    = []byte("EXTENDED CPC DSK File")
	trackInfoSignature = []byte("Track-Info\r\n")
)

// ParseDiskImage parses an in-memory disk image of any supported format,
// dispatching by signature. Headerless formats (MGT / IMG / TRD /
// D40 / D80) have no magic bytes — the caller resolves those via
// loadDiskByPath's file-extension fallback.
func ParseDiskImage(data []byte) (*Disk, error) {
	switch {
	case bytes.HasPrefix(data, dskSignatureStd),
		bytes.HasPrefix(data, dskSignatureExt):
		return ParseDSK(data)
	case bytes.HasPrefix(data, udiSignature):
		return ParseUDI(data)
	case bytes.HasPrefix(data, sadSignature):
		return ParseSAD(data)
	}
	return nil, fmt.Errorf("unrecognised disk image: first 16 bytes %x", data[:min(16, len(data))])
}

// ParseDSK parses a DSK image from a byte slice. Both the original
// CPCEMU "MV - CPCEMU Disk-File" format and the EXTENDED CPC DSK
// variant are supported. The bytes are parsed into the track byte
// stream model — sector data is laid out as if read from a spinning
// floppy, with sync fields, address marks, CRCs, and gaps.
func ParseDSK(data []byte) (*Disk, error) {
	if len(data) < dskHeaderSize {
		return nil, fmt.Errorf("DSK too short: %d bytes", len(data))
	}

	var extended bool
	switch {
	case bytes.HasPrefix(data, dskSignatureStd):
		extended = false
	case bytes.HasPrefix(data, dskSignatureExt):
		extended = true
	default:
		return nil, fmt.Errorf("not a DSK image: signature %q", data[:21])
	}

	cylinders := int(data[0x30])
	sides := int(data[0x31])
	if sides < 1 || sides > 2 {
		return nil, fmt.Errorf("DSK: invalid side count %d", sides)
	}
	if cylinders < 1 || cylinders > 84 {
		return nil, fmt.Errorf("DSK: invalid cylinder count %d", cylinders)
	}

	var trackLen []int
	if !extended {
		fixed := int(binary.LittleEndian.Uint16(data[0x32:0x34]))
		if fixed < trackHeaderSize {
			return nil, fmt.Errorf("DSK: track size %d below header size", fixed)
		}
		trackLen = make([]int, sides*cylinders)
		for i := range trackLen {
			trackLen[i] = fixed
		}
	} else {
		need := 0x34 + sides*cylinders
		if len(data) < need {
			return nil, fmt.Errorf("EDSK: header too short for %d tracks", sides*cylinders)
		}
		trackLen = make([]int, sides*cylinders)
		for i := range trackLen {
			trackLen[i] = int(data[0x34+i]) * 256
		}
	}

	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}

	offset := dskHeaderSize
	for i, tlen := range trackLen {
		if tlen == 0 {
			continue // EDSK: missing track
		}
		if offset+tlen > len(data) {
			return nil, fmt.Errorf("DSK: track %d (length %d) runs past end of file", i, tlen)
		}
		if tlen < trackHeaderSize {
			return nil, fmt.Errorf("DSK: track %d shorter than header (%d bytes)", i, tlen)
		}
		trackData := data[offset : offset+tlen]

		track, err := buildTrackFromDSK(trackData, extended)
		if err != nil {
			return nil, fmt.Errorf("track %d: %w", i, err)
		}
		h := int(track.H)
		c := int(track.C)
		if h >= sides || c >= cylinders {
			h = i % sides
			c = i / sides
		}
		d.Tracks[h][c] = track

		offset += tlen
	}

	return d, nil
}

// buildTrackFromDSK reads one Track-Info block from a DSK file and
// constructs a track-level Track byte stream from its sectors. trackData
// must include the 256-byte Track-Info header. The output uses the
// IBM34 MFM gap layout, matching what the +3 hardware emits when
// formatting a fresh disk.
func buildTrackFromDSK(trackData []byte, extended bool) (*Track, error) {
	if len(trackData) < trackHeaderSize {
		return nil, fmt.Errorf("track shorter than header")
	}
	if !bytes.HasPrefix(trackData, trackInfoSignature) {
		return nil, fmt.Errorf("missing Track-Info signature")
	}

	cyl := trackData[0x10]
	head := trackData[0x11]
	sectorN := trackData[0x14]
	numSectors := int(trackData[0x15])
	filler := trackData[0x17]

	if sectorInfoOffset+numSectors*sectorInfoSize > trackHeaderSize {
		return nil, fmt.Errorf("sector info table overflows track header (%d sectors)", numSectors)
	}

	t := newTrack(bytesPerTrackDD, filler, cyl, head, sectorN)
	b := newTrackBuilder(t, gapMFM)

	if !b.preindexAdd() {
		return nil, fmt.Errorf("track too small for pre-index gap")
	}
	if !b.postindexAdd() {
		return nil, fmt.Errorf("track too small for post-index gap")
	}

	// Walk the sector info table; for each sector emit ID + data.
	pos := trackHeaderSize
	for j := 0; j < numSectors; j++ {
		base := sectorInfoOffset + j*sectorInfoSize
		c := trackData[base+0]
		h := trackData[base+1]
		r := trackData[base+2]
		n := trackData[base+3]
		st1 := trackData[base+4]
		st2 := trackData[base+5]
		var dataBytes int
		if extended {
			dataBytes = int(binary.LittleEndian.Uint16(trackData[base+6 : base+8]))
		} else {
			dataBytes = sectorLength(n)
		}
		if pos+dataBytes > len(trackData) {
			return nil, fmt.Errorf("sector %d data extends past track end", j)
		}
		sectorData := trackData[pos : pos+dataBytes]

		// EDSK ST1/ST2 bits propagate to CRC error markings.
		idCRCError := st1&0x20 != 0 // ST1 bit 5: data CRC error in ID field
		dataCRCErr := st2&0x20 != 0 // ST2 bit 5: data error in data field
		deleted := st2&0x40 != 0    // ST2 bit 6: control mark (deleted DAM)

		if !b.idAdd(c, h, r, n, idCRCError) {
			return nil, fmt.Errorf("track too small to write sector %d ID", j)
		}
		// EDSK encodes "no data field" with a stored length of 0 —
		// see extractTrackSectors in save.go for the symmetric save
		// path. Skip dataAdd entirely so a subsequent READ DATA on
		// this sector reports ST1.MA / ST1.ND instead of streaming
		// the bytes that physically follow the IDAM (which would be
		// the next sector's sync field or GAP IV).
		if dataBytes == 0 {
			continue
		}
		// EDSK can record more data bytes than the sector ID claims
		// (e.g. for weak sectors that store multiple copies). Emit only
		// the ID-length-worth and mark that range weak, so each read
		// returns different data the way a real weak/copy-protected
		// sector does.
		idLen := sectorLength(n)
		emitLen := dataBytes
		if emitLen > idLen {
			emitLen = idLen
		}
		dataStart, ok := b.dataAdd(sectorData[:emitLen], deleted, dataCRCErr)
		if !ok {
			return nil, fmt.Errorf("track too small to write sector %d data", j)
		}
		if dataBytes > idLen {
			b.markWeakRange(dataStart, emitLen)
		}

		pos += dataBytes
	}

	if !b.gap4Add() {
		return nil, fmt.Errorf("track too small for GAP IV")
	}

	return t, nil
}
