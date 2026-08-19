package sam

import (
	"strings"
	"testing"
)

// Extended DSK images of SAM disks.
//
// EDSK is the format SAMdisk writes and, as SimCoupe's manual puts it, "a
// flexible format able to represent all existing SAM disks" — it carries a
// per-track sector list rather than assuming one geometry for the whole disk,
// so it can hold the custom formats that MGT cannot.
//
// The SAM's own Disk is a flat sector store with ONE geometry, which is what
// its WD1772 addresses. So an EDSK is converted, and an image whose geometry
// varies from track to track is REFUSED with the track named rather than
// flattened into something that reads plausibly and is wrong.

// edskTrack describes one track for the fixture writer.
type edskTrack struct {
	cyl, head int
	sectors   []edskSector
}

type edskSector struct {
	c, h, r, n byte
	data       []byte
}

// buildEDSK writes an Extended DSK image by hand.
//
// The bytes are laid out here rather than through plus3fdc's own FormatTrack
// because that path sizes a track to the +3's nominal double-density capacity,
// which holds nine 512-byte sectors — a SAM track has ten. Writing the image
// directly also means the conversion is tested against a real EDSK layout
// rather than round-tripping through the code that reads it.
//
//	0x00  "EXTENDED CPC DSK File\r\nDisk-Info\r\n"
//	0x30  cylinders            0x31  sides
//	0x34  track size table, one byte per track, in units of 256 bytes
//	then each track: a 256-byte Track-Info header followed by its sector data
//	  0x00  "Track-Info\r\n"
//	  0x10  cylinder   0x11  head   0x14  size code   0x15  sector count
//	  0x18  sector info, eight bytes each: C H R N ST1 ST2 length-lo length-hi
func buildEDSK(t *testing.T, cyls, sides int, tracks []edskTrack) []byte {
	t.Helper()
	const headerSize, trackHeaderSize, sectorInfoOffset = 256, 256, 0x18

	var bodies [][]byte
	for _, tr := range tracks {
		body := make([]byte, trackHeaderSize)
		copy(body, "Track-Info\r\n")
		body[0x10] = byte(tr.cyl)
		body[0x11] = byte(tr.head)
		body[0x15] = byte(len(tr.sectors))
		body[0x17] = 0xE5 // filler
		if len(tr.sectors) > 0 {
			body[0x14] = tr.sectors[0].n
		}
		for j, sec := range tr.sectors {
			base := sectorInfoOffset + j*8
			body[base+0], body[base+1] = sec.c, sec.h
			body[base+2], body[base+3] = sec.r, sec.n
			body[base+6] = byte(len(sec.data))
			body[base+7] = byte(len(sec.data) >> 8)
		}
		for _, sec := range tr.sectors {
			body = append(body, sec.data...)
		}
		// Track lengths are stored in units of 256, so pad up to a boundary.
		if rem := len(body) % 256; rem != 0 {
			body = append(body, make([]byte, 256-rem)...)
		}
		if len(body)/256 > 0xFF {
			t.Fatalf("track %d/%d is %d bytes, too long for the size table", tr.cyl, tr.head, len(body))
		}
		bodies = append(bodies, body)
	}

	out := make([]byte, headerSize)
	copy(out, "EXTENDED CPC DSK File\r\nDisk-Info\r\n")
	copy(out[0x22:], "zx_go test")
	out[0x30], out[0x31] = byte(cyls), byte(sides)
	for i, b := range bodies {
		out[0x34+i] = byte(len(b) / 256)
	}
	for _, b := range bodies {
		out = append(out, b...)
	}
	return out
}

// uniformEDSK builds an image whose every track has the same geometry, with
// each sector filled from a function of its address so a sector landing at the
// wrong offset is visible in the bytes.
func uniformEDSK(t *testing.T, cyls, heads, sectors, sectorSize int) []byte {
	t.Helper()
	n := sizeCodeFor(t, sectorSize)
	var tracks []edskTrack
	for c := 0; c < cyls; c++ {
		for h := 0; h < heads; h++ {
			tr := edskTrack{cyl: c, head: h}
			for s := 1; s <= sectors; s++ {
				data := make([]byte, sectorSize)
				for i := range data {
					data[i] = sectorFill(c, h, s, i)
				}
				tr.sectors = append(tr.sectors, edskSector{
					c: byte(c), h: byte(h), r: byte(s), n: n, data: data,
				})
			}
			tracks = append(tracks, tr)
		}
	}
	return buildEDSK(t, cyls, heads, tracks)
}

// sectorFill is a distinct byte per (cylinder, head, sector, offset), so a
// sector read from the wrong track, side or position is visible in the value.
func sectorFill(cyl, head, sector, off int) byte {
	return byte(cyl*7 + head*53 + sector*11 + off*3)
}

func sizeCodeFor(t *testing.T, size int) byte {
	t.Helper()
	switch size {
	case 128:
		return 0
	case 256:
		return 1
	case 512:
		return 2
	case 1024:
		return 3
	}
	t.Fatalf("no size code for %d-byte sectors", size)
	return 0
}

// A standard SAM disk in EDSK loads, with the geometry the image declares.
func TestAnEDSKLoadsWithItsOwnGeometry(t *testing.T) {
	blob := uniformEDSK(t, mgtCyls, mgtHeads, mgt800KSectors, mgtSectorSize)

	d, err := LoadDisk(blob)
	if err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}
	cyls, heads, sectors, size := d.Geometry()
	if cyls != mgtCyls || heads != mgtHeads || sectors != mgt800KSectors || size != mgtSectorSize {
		t.Errorf("geometry = %dx%dx%d @%d, want %dx%dx%d @%d",
			cyls, heads, sectors, size, mgtCyls, mgtHeads, mgt800KSectors, mgtSectorSize)
	}
}

// Every sector must land at the address it was written to. This is the whole
// content of the conversion: EDSK stores sectors by their ID within a track,
// and the flat store addresses them by offset, so getting the track order or
// the sector base wrong reads a plausible disk with the wrong contents.
func TestEverySectorLandsAtItsOwnAddress(t *testing.T) {
	const cyls, heads, sectors, size = 4, 2, 10, 512
	d, err := LoadDisk(uniformEDSK(t, cyls, heads, sectors, size))
	if err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}

	for c := 0; c < cyls; c++ {
		for h := 0; h < heads; h++ {
			for s := 1; s <= sectors; s++ {
				got, ok := d.ReadSector(c, h, s)
				if !ok {
					t.Fatalf("sector (%d,%d,%d) is missing", c, h, s)
				}
				for i := range got {
					if want := sectorFill(c, h, s, i); got[i] != want {
						t.Fatalf("sector (%d,%d,%d) byte %d = %#02x, want %#02x",
							c, h, s, i, got[i], want)
					}
				}
			}
		}
	}
}

// A track whose geometry differs from the rest is refused, and the message
// names it. The flat store has one sectors-per-track and one sector size for
// the whole disk, so there is nowhere to put a track that disagrees; accepting
// it would silently truncate or misplace the rest of the image.
func TestAVaryingGeometryIsRefusedAndNamed(t *testing.T) {
	const cyls, heads, sectors, size = 3, 2, 10, 512
	var tracks []edskTrack
	for c := 0; c < cyls; c++ {
		for h := 0; h < heads; h++ {
			count := sectors
			if c == 2 && h == 1 {
				count = sectors - 3 // the odd track out
			}
			tr := edskTrack{cyl: c, head: h}
			for sec := 1; sec <= count; sec++ {
				tr.sectors = append(tr.sectors, edskSector{
					c: byte(c), h: byte(h), r: byte(sec), n: 2, data: make([]byte, size),
				})
			}
			tracks = append(tracks, tr)
		}
	}

	_, err := LoadDisk(buildEDSK(t, cyls, heads, tracks))
	if err == nil {
		t.Fatal("an image with a varying track geometry was accepted")
	}
	if !strings.Contains(err.Error(), "cylinder 2") || !strings.Contains(err.Error(), "head 1") {
		t.Errorf("error = %q, want it to name cylinder 2 head 1", err)
	}
}

// A non-uniform sector SIZE is refused for the same reason, and separately,
// because a track can agree on the count and disagree on the size.
func TestAVaryingSectorSizeIsRefused(t *testing.T) {
	var tracks []edskTrack
	for c := 0; c < 2; c++ {
		n, size := byte(2), 512
		if c == 1 {
			n, size = 1, 256
		}
		tr := edskTrack{cyl: c}
		for sec := 1; sec <= 4; sec++ {
			tr.sectors = append(tr.sectors, edskSector{
				c: byte(c), r: byte(sec), n: n, data: make([]byte, size),
			})
		}
		tracks = append(tracks, tr)
	}

	if _, err := LoadDisk(buildEDSK(t, 2, 1, tracks)); err == nil {
		t.Fatal("an image with two sector sizes was accepted")
	}
}

// Sector IDs outside 1..N have nowhere to go in a flat store addressed by
// position, so they are refused rather than dropped.
func TestOutOfRangeSectorIDsAreRefused(t *testing.T) {
	tracks := []edskTrack{{sectors: []edskSector{
		{r: 1, n: 2, data: make([]byte, 512)},
		{r: 200, n: 2, data: make([]byte, 512)}, // way past the track's own count
	}}}

	if _, err := LoadDisk(buildEDSK(t, 1, 1, tracks)); err == nil {
		t.Fatal("a sector numbered outside the track was accepted")
	}
}

// An unformatted track is media, not a bad image. EDSK stores one as a zero
// length, and real dumps carry them: an outer cylinder never written, or a
// damaged one the dumper could not read. Refusing the whole disk over one meant
// the user got nothing; the flat store simply keeps its zeros there, which is
// what the guest reads off blank media anyway.
func TestAnUnformattedTrackDoesNotRejectTheDisk(t *testing.T) {
	const cyls, heads, sectors, size = 4, 2, 10, 512
	var tracks []edskTrack
	for c := 0; c < cyls; c++ {
		for h := 0; h < heads; h++ {
			tr := edskTrack{cyl: c, head: h}
			if c == 3 && h == 1 {
				tracks = append(tracks, tr) // no sectors: an unformatted track
				continue
			}
			for s := 1; s <= sectors; s++ {
				data := make([]byte, size)
				for i := range data {
					data[i] = sectorFill(c, h, s, i)
				}
				tr.sectors = append(tr.sectors, edskSector{
					c: byte(c), h: byte(h), r: byte(s), n: 2, data: data,
				})
			}
			tracks = append(tracks, tr)
		}
	}

	d, err := LoadDisk(buildEDSK(t, cyls, heads, tracks))
	if err != nil {
		t.Fatalf("LoadDisk refused a disk with one unformatted track: %v", err)
	}
	// The formatted tracks still read correctly...
	got, ok := d.ReadSector(0, 0, 1)
	if !ok {
		t.Fatal("sector (0,0,1) is missing")
	}
	if got[0] != sectorFill(0, 0, 1, 0) {
		t.Errorf("sector (0,0,1) byte 0 = %#02x, want %#02x", got[0], sectorFill(0, 0, 1, 0))
	}
	// ...and the unformatted one reads as blank rather than as another track's
	// data shifted into its place.
	blank, ok := d.ReadSector(3, 1, 1)
	if !ok {
		t.Fatal("the unformatted track is not addressable at all")
	}
	for i, v := range blank {
		if v != 0 {
			t.Fatalf("unformatted sector byte %d = %#02x, want 0: something else landed there", i, v)
		}
	}
}

// MGT and SAD keep working. The EDSK branch is chosen by signature, so a
// headerless MGT must not be dragged into it.
func TestTheExistingFormatsStillLoad(t *testing.T) {
	if _, err := LoadDisk(make([]byte, mgt800KSize)); err != nil {
		t.Errorf("800K MGT: %v", err)
	}
	if _, err := LoadDisk(make([]byte, mgt720KSize)); err != nil {
		t.Errorf("720K MGT: %v", err)
	}
	sad := make([]byte, sadHeaderLen+2*2*10*512)
	copy(sad, sadSignature)
	sad[18], sad[19], sad[20], sad[21] = 2, 2, 10, 512/sadSizeDivisor
	if _, err := LoadDisk(sad); err != nil {
		t.Errorf("SAD: %v", err)
	}
}

// An image claiming to be EDSK but truncated is an error rather than a partial
// disk. The parser is shared with the +3, so this checks the error surfaces
// rather than re-testing the parser.
func TestATruncatedEDSKIsAnError(t *testing.T) {
	blob := uniformEDSK(t, 2, 1, 10, 512)
	if _, err := LoadDisk(blob[:len(blob)/2]); err == nil {
		t.Fatal("a truncated EDSK was accepted")
	}
}
