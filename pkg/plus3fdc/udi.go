package plus3fdc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// UDI ("UDI!") is a raw-track disk image format. Unlike DSK it
// preserves the full byte-stream view of each track including clock
// marks, MFM/FM marks, and weak data bits — which is why it's the only
// format handled here that reliably round-trips copy-protected disks.
//
// File layout:
//
//	 0  4  "UDI!"
//	 4  4  file length - 4  (little-endian uint32)
//	 8  1  version (0)
//	 9  1  cylinders - 1
//	10  1  sides - 1
//	11  1  reserved
//	12  4  reserved
//	16  .. tracks
//	... 4  CRC32 (not verified by this parser)
//
// Per-track layout:
//
//	0  1  type (0x00 = FM, 0x01 = MFM basic, 0x02 = MFM with FM/weak)
//	1  2  bpt (bytes per track)
//	3  bpt bytes of track data
//	3+bpt  bpt/8 bytes of clock marks
//	3+bpt + bpt/8  (MFM only) bpt/8 bytes of FM marks
//	3+bpt + 2*(bpt/8)  (type 0x02 only) bpt/8 bytes of weak marks
//
// We accept types 0x00, 0x01, 0x02, and their 0x80-0x82 "already-read"
// aliases. Compressed tracks (0xF0) and the multi-read weak-sector
// block type (0x83) are not supported — they're rare and require
// additional decoder state.
const (
	udiTypeFM      = 0x00
	udiTypeMFM     = 0x01
	udiTypeMFMWeak = 0x02
)

var udiSignature = []byte("UDI!")

// ParseUDI parses a UDI image from a byte slice.
func ParseUDI(data []byte) (*Disk, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("UDI too short: %d bytes", len(data))
	}
	if !bytes.HasPrefix(data, udiSignature) {
		return nil, fmt.Errorf("not a UDI image: signature %q", data[:4])
	}
	eof := int(binary.LittleEndian.Uint32(data[4:8]))
	if eof+4 != len(data) {
		return nil, fmt.Errorf("UDI size mismatch: header %d vs actual %d", eof+4, len(data))
	}
	cylinders := int(data[9]) + 1
	sides := int(data[10]) + 1
	if sides < 1 || sides > 2 {
		return nil, fmt.Errorf("UDI: invalid side count %d", sides)
	}
	if cylinders < 1 || cylinders > 84 {
		return nil, fmt.Errorf("UDI: invalid cylinder count %d", cylinders)
	}

	d := &Disk{
		Sides:     sides,
		Cylinders: cylinders,
		Tracks:    make([][]*Track, sides),
	}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cylinders)
	}

	// UDI tracks are stored in (cylinder, head) order.
	offset := 16
	for i := 0; i < sides*cylinders; i++ {
		if offset+3 > eof {
			return nil, fmt.Errorf("UDI track %d header runs past end", i)
		}
		ttyp := data[offset]
		bpt := int(binary.LittleEndian.Uint16(data[offset+1 : offset+3]))
		if bpt == 0 {
			offset += 3
			continue
		}

		base := ttyp & 0x7F // strip the "already read" flag
		if base != udiTypeFM && base != udiTypeMFM && base != udiTypeMFMWeak {
			return nil, fmt.Errorf("UDI track %d: unsupported type 0x%02X", i, ttyp)
		}

		clen := (bpt + 7) / 8
		var payload int
		switch base {
		case udiTypeFM, udiTypeMFM:
			payload = bpt + clen
		case udiTypeMFMWeak:
			payload = bpt + 3*clen
		}
		trackBytes := 3 + payload
		if offset+trackBytes > eof {
			return nil, fmt.Errorf("UDI track %d data runs past end", i)
		}

		cyl := byte(i / sides)
		head := byte(i % sides)
		t := &Track{
			C:      cyl,
			H:      head,
			bpt:    bpt,
			data:   make([]byte, bpt),
			clocks: make([]byte, clen),
			weak:   make([]byte, clen),
		}
		copy(t.data, data[offset+3:offset+3+bpt])
		copy(t.clocks, data[offset+3+bpt:offset+3+bpt+clen])
		if base == udiTypeMFMWeak {
			// UDI type 0x02 payload: clocks, then FM marks (which
			// we skip — FM decoding isn't modelled), then weak
			// marks.
			copy(t.weak, data[offset+3+bpt+2*clen:offset+3+bpt+3*clen])
		}

		d.Tracks[head][cyl] = t
		offset += trackBytes
	}

	return d, nil
}
