package plus3fdc

import (
	"encoding/binary"
	"testing"
)

// A protected +3 track shape that must keep parsing: nine ordinary 512-byte
// sectors plus one ID-only sector — EDSK stored length 0 — whose declared size
// code is large (N=6 implies 8192 bytes).
//
// It builds fine because an ID-only sector emits its ID field and no data at
// all. Sizing the track budget from the largest *declared* sector instead of
// the data actually emitted counts that phantom 8192 ten times over, blows the
// budget, and rejects the whole disk. This is a regression guard for exactly
// that: the gap-tightening added in v1.8.4 originally made this mistake.
func edskWithIDOnlySector() []byte {
	const trackLen = 256 + 9*512 // 4864

	data := make([]byte, dskHeaderSize)
	copy(data, "EXTENDED CPC DSK File\r\nDisk-Info\r\n")
	data[0x30] = 1 // cylinders
	data[0x31] = 1 // sides
	data[0x34] = byte(trackLen / 256)

	tr := make([]byte, trackLen)
	copy(tr, trackInfoSignature)
	tr[0x14] = 2  // nominal sector size code
	tr[0x15] = 10 // sector count
	tr[0x17] = 0xE5
	for j := 0; j < 9; j++ {
		b := sectorInfoOffset + j*sectorInfoSize
		tr[b+2] = byte(j + 1) // R
		tr[b+3] = 2           // N = 512
		binary.LittleEndian.PutUint16(tr[b+6:b+8], 512)
	}
	// The ID-only sector: large N, zero stored data.
	b := sectorInfoOffset + 9*sectorInfoSize
	tr[b+2] = 0xC1
	tr[b+3] = 6 // N = 6 implies 8192 bytes that are not there
	binary.LittleEndian.PutUint16(tr[b+6:b+8], 0)

	return append(data, tr...)
}

func TestParseDSKKeepsIDOnlySectorTracks(t *testing.T) {
	d, err := ParseDSK(edskWithIDOnlySector())
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	if d.Cylinders != 1 || d.Sides != 1 {
		t.Fatalf("geometry = %dx%d, want 1x1", d.Cylinders, d.Sides)
	}
	if d.Tracks[0][0] == nil {
		t.Fatal("track 0 was not built")
	}
}

// A copy-protection track that must load: one oversized sector whose data
// overlaps the ordinary sectors that follow it.
//
// Adidas Championship Tie-Break (Ocean 1990) declares track 1 as eleven
// sectors — R=0xC1 with N=6 storing 6144 bytes, then ten 512-byte sectors —
// totalling 11264 bytes of sector data. That does not fit a nominal
// double-density track, and on real hardware it does not have to: the big
// sector's data IS the region the small ones occupy, read back through a
// different ID. It is the classic Speedlock-style overlapping-sector scheme.
//
// A flat byte-stream model cannot overlap them, but the image says what the
// track holds — its own track table declares 11520 bytes — and the guest only
// ever reads by sector ID. Laying them out in a track sized to what the image
// describes therefore returns the right bytes for every read, where refusing
// the disk returns nothing at all. Eight images in a 250-disk sample failed
// this way.
func edskOverlappingProtectionTrack() []byte {
	const trackLen = 11520
	data := make([]byte, dskHeaderSize)
	copy(data, "EXTENDED CPC DSK File\r\nDisk-Info\r\n")
	data[0x30] = 1 // cylinders
	data[0x31] = 1 // sides
	data[0x34] = byte(trackLen / 256)

	tr := make([]byte, trackLen)
	copy(tr, trackInfoSignature)
	tr[0x10] = 1  // C
	tr[0x14] = 6  // nominal N
	tr[0x15] = 11 // sector count
	tr[0x17] = 0xE5

	put := func(j int, r, n byte, stored int) {
		b := sectorInfoOffset + j*sectorInfoSize
		tr[b+0] = 1 // C
		tr[b+2] = r
		tr[b+3] = n
		tr[b+6] = byte(stored & 0xFF)
		tr[b+7] = byte(stored >> 8)
	}
	put(0, 0xC1, 6, 6144) // the oversized, overlapping sector
	for j := 1; j < 11; j++ {
		put(j, byte(0x40+j), 2, 512)
	}
	return append(data, tr...)
}

func TestParseDSKLoadsOverlappingProtectionTracks(t *testing.T) {
	d, err := ParseDSK(edskOverlappingProtectionTrack())
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	tr := d.Tracks[0][0]
	if tr == nil {
		t.Fatal("the track was not built")
	}
	// Every declared sector must be findable by its ID, which is the only way
	// the guest ever reaches them.
	for _, r := range []byte{0xC1, 0x41, 0x45, 0x4A} {
		if tr.FindSector(r) == nil {
			t.Errorf("sector R=%#02x is not present on the rebuilt track", r)
		}
	}
}
