package sam

import (
	"bytes"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
)

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

// LoadDisk parses a SAM disk image.
//
// The format is chosen by signature where there is one — SAD and Extended DSK
// both carry a header — and by size otherwise, since MGT is a bare sector dump
// with nothing to identify it.
//
// SBT is not supported, deliberately. SimCoupe's manual describes those as
// "self-booting files designed to be copied to an empty SAM disk, then booted"
// and notes they are "not technically disk images"; where in a blank disk the
// file goes, and what directory entry is written for it, is not published
// anywhere this project can use. The only description of it is in another
// emulator's GPL source, and reading that to derive it is the thing this
// project's licence rules exist to prevent. It stays unsupported until a
// specification turns up rather than being guessed at.
func LoadDisk(data []byte) (*Disk, error) {
	if len(data) >= len(sadSignature) && string(data[:len(sadSignature)]) == sadSignature {
		return loadSAD(data)
	}
	if isEDSK(data) {
		return loadEDSK(data)
	}
	switch len(data) {
	case mgt800KSize:
		return loadMGT(data, mgt800KSectors), nil
	case mgt720KSize:
		return loadMGT(data, mgt720KSectors), nil
	}
	return nil, fmt.Errorf("sam: unrecognised disk image (%d bytes; not MGT, SAD or EDSK)", len(data))
}

// isEDSK reports whether data carries one of the two DSK signatures. Only the
// first eight bytes of the standard one are fixed: the rest of the description
// field is free text chosen by whichever tool wrote the image.
func isEDSK(data []byte) bool {
	return bytes.HasPrefix(data, []byte("MV - CPC")) ||
		bytes.HasPrefix(data, []byte("EXTENDED CPC DSK File"))
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

// loadEDSK converts an Extended DSK image into the SAM's flat sector store.
//
// The parser is the one the +3 already uses (pkg/plus3fdc): EDSK is EDSK
// whichever machine wrote it, and it is a format this project already reads
// correctly, including the awkward cases — variable track lengths, oversized
// sectors, weak-sector duplicate copies.
//
// What differs is the destination. plus3fdc keeps a byte-stream track model
// with a per-track sector list, which is what a uPD765 addresses. The SAM's
// Disk is a flat store with ONE geometry for the whole disk, which is what its
// WD1772 addresses through Disk.offset. So the conversion has to establish a
// single geometry, and an image that does not have one is REFUSED with the
// offending track named.
//
// That refusal is the important half. Flattening a disk whose tracks disagree
// would place every sector after the odd one at the wrong offset: the image
// would load, most of it would read correctly, and the failure would surface as
// a corrupt file somewhere in the middle of a game.
func loadEDSK(data []byte) (*Disk, error) {
	parsed, err := plus3fdc.ParseDiskImage(data)
	if err != nil {
		return nil, fmt.Errorf("sam: %w", err)
	}
	if parsed.Cylinders < 1 || parsed.Sides < 1 {
		return nil, fmt.Errorf("sam: EDSK declares %d cylinders and %d sides",
			parsed.Cylinders, parsed.Sides)
	}

	sectors, sectorSize := 0, 0
	type placed struct {
		cyl, head, index int
		data             []byte
	}
	var all []placed

	for c := 0; c < parsed.Cylinders; c++ {
		for h := 0; h < parsed.Sides; h++ {
			track := parsed.Track(h, c)
			if track == nil {
				return nil, fmt.Errorf("sam: EDSK has no track at cylinder %d head %d, "+
					"which a flat SAM image cannot represent", c, h)
			}
			// Walk the track's OWN ID list rather than probing for sector 1, 2,
			// 3 and so on. Probing sees only the prefix of numbers that happen
			// to exist, so a track carrying sectors 1 and 200 looks like a
			// one-sector track and the 200 vanishes without a word.
			ids, err := trackSectorIDs(track)
			if err != nil {
				return nil, fmt.Errorf("sam: EDSK cylinder %d head %d: %w", c, h, err)
			}
			if len(ids) == 0 {
				return nil, fmt.Errorf("sam: EDSK cylinder %d head %d has no sectors", c, h)
			}

			if sectors == 0 {
				sectors = len(ids)
			}
			if len(ids) != sectors {
				return nil, fmt.Errorf("sam: EDSK cylinder %d head %d has %d sectors, "+
					"the rest of the disk has %d; a flat SAM image has one geometry",
					c, h, len(ids), sectors)
			}

			// The IDs have to be exactly 1..N. A flat store addresses a sector
			// by its position in the track, so there is nowhere to put one
			// numbered outside that range, and a duplicate would overwrite its
			// twin.
			seen := make(map[byte]bool, len(ids))
			for _, id := range ids {
				if id < 1 || int(id) > sectors {
					return nil, fmt.Errorf("sam: EDSK cylinder %d head %d numbers a sector %d, "+
						"outside the 1..%d a flat SAM image can address", c, h, id, sectors)
				}
				if seen[id] {
					return nil, fmt.Errorf("sam: EDSK cylinder %d head %d has two sectors numbered %d",
						c, h, id)
				}
				seen[id] = true
			}

			for _, id := range ids {
				sec := track.FindSector(id)
				if sec == nil {
					return nil, fmt.Errorf("sam: EDSK cylinder %d head %d lists sector %d "+
						"with no readable data", c, h, id)
				}
				if sectorSize == 0 {
					sectorSize = len(sec.Data)
				}
				if len(sec.Data) != sectorSize {
					return nil, fmt.Errorf("sam: EDSK cylinder %d head %d sector %d is %d bytes, "+
						"the rest of the disk uses %d; a flat SAM image has one sector size",
						c, h, sec.R, len(sec.Data), sectorSize)
				}
				all = append(all, placed{cyl: c, head: h, index: int(sec.R) - 1, data: sec.Data})
			}
		}
	}
	if sectorSize < 128 {
		return nil, fmt.Errorf("sam: EDSK sector size %d is below the 128-byte minimum", sectorSize)
	}

	d := &Disk{
		cyls:            parsed.Cylinders,
		heads:           parsed.Sides,
		sectorsPerTrack: sectors,
		sectorSize:      sectorSize,
		headMajor:       false, // cylinder-major, as MGT lays a disk out
		data:            make([]byte, parsed.Cylinders*parsed.Sides*sectors*sectorSize),
	}
	for _, p := range all {
		off, ok := d.offset(p.cyl, p.head, p.index+1)
		if !ok {
			return nil, fmt.Errorf("sam: EDSK cylinder %d head %d sector %d falls outside "+
				"the %dx%d geometry", p.cyl, p.head, p.index+1, d.cyls, d.heads)
		}
		copy(d.data[off:off+sectorSize], p.data)
	}
	return d, nil
}

// maxSAMSectorsPerTrack bounds the ID walk. Real SAM disks use ten sectors a
// track and custom formats a few more; this is a loop bound rather than a
// format limit, high enough that no genuine image reaches it.
const maxSAMSectorsPerTrack = 64

// trackSectorIDs returns the sector IDs a track carries, in the physical order
// they appear. Reading the track's own address marks is what makes an
// interleaved layout — sectors stored out of numerical order, which is how a
// real disk is written for speed — come back correctly.
func trackSectorIDs(track *plus3fdc.Track) ([]byte, error) {
	var ids []byte
	for pos := 0; ; {
		_, _, r, _, idEnd, ok := track.IdAt(pos)
		if !ok {
			return ids, nil
		}
		ids = append(ids, r)
		if len(ids) > maxSAMSectorsPerTrack {
			return nil, fmt.Errorf("more than %d sectors, which no SAM format uses",
				maxSAMSectorsPerTrack)
		}
		pos = idEnd
	}
}
