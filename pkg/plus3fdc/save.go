package plus3fdc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

// SaveDSK serialises the in-memory Disk back to an EDSK file on disk.
// The format used is always "EXTENDED CPC DSK File" because it cleanly
// handles variable-length tracks, missing tracks, and per-sector error
// flags — all features we need to preserve when round-tripping modified
// disks.
func (d *Disk) SaveDSK(path string) error {
	data, err := d.SerializeDSK()
	if err != nil {
		return fmt.Errorf("serialise DSK: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// SerializeDSK returns the EDSK bytes for this disk. Each track's
// sectors are walked from the byte stream, CRCs are recomputed to
// detect imaging-time errors, and the deleted-DAM bit flows into ST2
// of the sector info table. Weak sectors are NOT preserved as multiple
// data copies (that would require format-specific handling outside the
// scope of this writer); the current byte pattern is what gets saved.
func (d *Disk) SerializeDSK() ([]byte, error) {
	type trackOut struct {
		present bool
		header  [trackHeaderSize]byte
		data    []byte
	}
	tracks := make([]trackOut, d.Sides*d.Cylinders)

	// Walk tracks in (cylinder, head) order so the on-disk layout
	// matches what ParseDSK / buildSyntheticDSK produce and loaders
	// without header-coordinate fallbacks still see a sane geometry.
	for c := 0; c < d.Cylinders; c++ {
		for h := 0; h < d.Sides; h++ {
			idx := c*d.Sides + h
			t := d.Track(h, c)
			if t == nil {
				continue
			}
			sectors, err := extractTrackSectors(t)
			if err != nil {
				return nil, fmt.Errorf("track c=%d h=%d: %w", c, h, err)
			}
			// The track-info header is 256 bytes and sector entries
			// start at 0x18 with 8 bytes each, so the theoretical
			// cap is (256-0x18)/8 = 29 sectors. Anything beyond that
			// can't be represented in a DSK file.
			const maxSectorsPerTrack = (trackHeaderSize - sectorInfoOffset) / sectorInfoSize
			if len(sectors) > maxSectorsPerTrack {
				return nil, fmt.Errorf("track c=%d h=%d has %d sectors, max %d fit in DSK header",
					c, h, len(sectors), maxSectorsPerTrack)
			}

			var out trackOut
			out.present = true
			copy(out.header[:], trackInfoSignature)
			out.header[0x10] = t.C
			out.header[0x11] = t.H
			out.header[0x14] = t.N
			out.header[0x15] = byte(len(sectors))
			out.header[0x16] = 0x4E // GAP3 (cosmetic; parser ignores it)
			out.header[0x17] = t.Filler

			var buf bytes.Buffer
			for j, s := range sectors {
				base := sectorInfoOffset + j*sectorInfoSize
				out.header[base+0] = s.c
				out.header[base+1] = s.h
				out.header[base+2] = s.r
				out.header[base+3] = s.n
				out.header[base+4] = s.st1
				out.header[base+5] = s.st2
				binary.LittleEndian.PutUint16(out.header[base+6:base+8], uint16(len(s.data)))
				buf.Write(s.data)
			}
			out.data = buf.Bytes()
			tracks[idx] = out
		}
	}

	// EDSK track lengths must be multiples of 256 (the header stores
	// length/256 per track). Pad each track's data area with zeros if
	// needed.
	lens := make([]int, len(tracks))
	for i, t := range tracks {
		if !t.present {
			continue
		}
		total := trackHeaderSize + len(t.data)
		if total%256 != 0 {
			total += 256 - (total % 256)
		}
		lens[i] = total
	}

	// Main 256-byte disk header.
	var out bytes.Buffer
	hdr := make([]byte, dskHeaderSize)
	copy(hdr, dskSignatureExt)
	// 14-byte creator / "disk name" field — informational only.
	copy(hdr[0x22:0x30], []byte("zx_go         "))
	hdr[0x30] = byte(d.Cylinders)
	hdr[0x31] = byte(d.Sides)
	for i, l := range lens {
		if 0x34+i >= len(hdr) {
			return nil, fmt.Errorf("too many tracks (%d) to fit in EDSK header table", len(lens))
		}
		hdr[0x34+i] = byte(l / 256)
	}
	out.Write(hdr)

	// Emit each track's 256-byte Track-Info header followed by the
	// packed sector data, padded to the declared track length.
	for i, t := range tracks {
		if !t.present {
			continue
		}
		out.Write(t.header[:])
		out.Write(t.data)
		pad := lens[i] - trackHeaderSize - len(t.data)
		if pad > 0 {
			out.Write(make([]byte, pad))
		}
	}

	return out.Bytes(), nil
}

// sectorOut is the intermediate representation used by the DSK writer
// when walking a track's byte stream.
type sectorOut struct {
	c, h, r, n byte
	st1, st2   byte
	data       []byte
}

// extractTrackSectors walks the track byte stream and extracts each
// sector's CHRN + stored data + derived ST1/ST2 flags. The stored data
// is copied verbatim from t.data — NOT via readByte — so weak bytes
// come out as whatever pattern was present when the disk was loaded.
// CRCs are recomputed so that imaging-time errors (bad ID CRC, bad
// data CRC) round-trip through save/reload.
//
// IDAM-only sectors (a header with no following DAM) are preserved as
// a zero-length data field with ST1.DE set, so game protections that
// rely on header-without-data round-trip through save/reload.
func extractTrackSectors(t *Track) ([]sectorOut, error) {
	var out []sectorOut
	pos := 0
	for {
		c, h, r, n, idEnd, idCRCOK, ok := t.idAtCRC(pos)
		if !ok {
			return out, nil
		}
		dataStart, deleted, dok := t.dataAt(idEnd)
		if !dok {
			// Header exists but no data field follows. Record the
			// IDAM anyway with an empty data field and the ST1.DE
			// (missing-data) bit set so reloading preserves the
			// anomaly.
			var st1 byte = 0x01 // ST1.MA (missing address mark)
			if !idCRCOK {
				st1 |= 0x20
			}
			out = append(out, sectorOut{
				c: c, h: h, r: r, n: n,
				st1:  st1,
				st2:  0,
				data: nil,
			})
			pos = idEnd
			continue
		}
		length := sectorLength(n)
		if dataStart+length > t.bpt {
			return nil, fmt.Errorf("sector R=%d data extends past track end", r)
		}
		data := make([]byte, length)
		copy(data, t.data[dataStart:dataStart+length])

		// Recompute the data CRC so an on-track mismatch flows into
		// ST2.DD. Deleted sectors prime the CRC with the F8 mark.
		crc := initialDataCRC(deleted)
		for _, by := range data {
			crc = crcUpdate(crc, by)
		}
		var st1, st2 byte
		if !idCRCOK {
			st1 |= 0x20 // Data error in ID field
		}
		if dataStart+length+2 <= t.bpt {
			storedHi := t.data[dataStart+length]
			storedLo := t.data[dataStart+length+1]
			if storedHi != byte(crc>>8) || storedLo != byte(crc&0xFF) {
				st2 |= 0x20 // Data error in data field
			}
		}
		if deleted {
			st2 |= 0x40 // Control mark (deleted DAM)
		}

		out = append(out, sectorOut{
			c: c, h: h, r: r, n: n,
			st1: st1, st2: st2,
			data: data,
		})
		pos = dataStart + length + 2
	}
}
