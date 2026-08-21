package plus3fdc

import (
	"encoding/binary"
	"testing"
)

// edskWithTrackLabels builds a 2-cylinder, 1-side EDSK whose Track-Info
// C/H labels are the ones given, in file order. The track-size table is
// the physical order, so track block i is cylinder i.
func edskWithTrackLabels(labels [][2]byte) []byte {
	const trackLen = 256 + 512 // header + one 512-byte sector

	data := make([]byte, dskHeaderSize)
	copy(data, "EXTENDED CPC DSK File\r\nDisk-Info\r\n")
	data[0x30] = byte(len(labels)) // cylinders
	data[0x31] = 1                 // sides
	for i := range labels {
		data[0x34+i] = byte(trackLen / 256)
	}

	for i, l := range labels {
		tr := make([]byte, trackLen)
		copy(tr, trackInfoSignature)
		tr[0x10] = l[0] // Track-Info C
		tr[0x11] = l[1] // Track-Info H
		tr[0x14] = 2    // nominal sector size code
		tr[0x15] = 1    // one sector
		tr[0x17] = 0xE5
		b := sectorInfoOffset
		tr[b+0] = l[0]        // sector C
		tr[b+1] = l[1]        // sector H
		tr[b+2] = byte(i + 1) // R: distinct per physical track
		tr[b+3] = 2
		binary.LittleEndian.PutUint16(tr[b+6:b+8], 512)
		data = append(data, tr...)
	}
	return data
}

// The DSK track-size table IS the physical order: block i is cylinder
// i/sides, head i%sides. We indexed d.Tracks by the Track-Info C/H
// labels whenever they happened to be in range, so a dump that tags
// every track C=0 — common in bad dumps and in copy protection — piled
// them all into one slot and left the rest of the disk empty.
func TestTracksAreIndexedByFileOrderNotByTheirLabels(t *testing.T) {
	// Both tracks claim to be cylinder 0.
	d, err := ParseDSK(edskWithTrackLabels([][2]byte{{0, 0}, {0, 0}}))
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	for c := 0; c < 2; c++ {
		tr := d.Track(0, c)
		if tr == nil {
			t.Fatalf("cylinder %d is empty: the second track overwrote the first", c)
		}
		_, _, r, _, _, ok := tr.IdAt(0)
		if !ok {
			t.Fatalf("cylinder %d has no sector IDs", c)
		}
		if r != byte(c+1) {
			t.Errorf("cylinder %d holds sector R=%d, want R=%d", c, r, c+1)
		}
	}
}

// A lying label must not move a track either.
func TestAnOutOfOrderLabelDoesNotMoveTheTrack(t *testing.T) {
	// Track block 0 claims to be cylinder 1 and block 1 claims cylinder 0.
	d, err := ParseDSK(edskWithTrackLabels([][2]byte{{1, 0}, {0, 0}}))
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	if _, _, r, _, _, _ := d.Track(0, 0).IdAt(0); r != 1 {
		t.Errorf("cylinder 0 holds sector R=%d, want R=1 (the first track block)", r)
	}
	if _, _, r, _, _, _ := d.Track(0, 1).IdAt(0); r != 2 {
		t.Errorf("cylinder 1 holds sector R=%d, want R=2 (the second track block)", r)
	}
}

// The labels themselves are still carried on the track, because the FDC
// matches an ID field's C against the command's cylinder.
func TestTrackKeepsItsDeclaredLabels(t *testing.T) {
	d, err := ParseDSK(edskWithTrackLabels([][2]byte{{9, 0}, {9, 0}}))
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	if got := d.Track(0, 0).C; got != 9 {
		t.Errorf("track 0 C = %d, want the declared 9", got)
	}
}
