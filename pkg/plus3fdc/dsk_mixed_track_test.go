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
