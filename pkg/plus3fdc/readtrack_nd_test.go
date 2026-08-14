package plus3fdc

import "testing"

// READ A TRACK does not search for a sector — it streams whatever the track
// holds from the index hole. It does still COMPARE, though, and that is the
// part we were missing.
//
// The uPD765A datasheet, READ A TRACK: "the FDC compares the ID information
// read from each sector with the value stored in the IDR and sets the ND flag
// of Status Register 1 to a 1 if there is no comparison."
//
// California Games is the evidence. Its loader reads its data cylinders
// normally, then recalibrates, seeks to cylinder 7 — a protection track whose
// sectors are numbered $B1-$B8 — and issues exactly one READ DIAGNOSTIC
// asking for R=01, which is not there. That is the last command it ever
// issues: it gives up on the answer. We were replying ST1=$80, end-of-cylinder
// alone, when the sector it named appears nowhere on the track.

// trackWithIDs builds a single-cylinder disk whose track carries sectors with
// the given IDs, which is how a protection track is laid out.
func trackWithIDs(ids []byte) *Disk {
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	for _, r := range ids {
		b.idAdd(0, 0, r, 2, false)
		b.dataAdd(make([]byte, 512), false, false)
	}
	b.gap4Add()
	d.Tracks[0][0] = tr
	return d
}

// readTrackResult runs one READ DIAGNOSTIC for the given sector ID and returns
// the seven result bytes.
func readTrackResult(t *testing.T, d *Disk, wantR byte) []byte {
	t.Helper()
	f := NewUPD765()
	f.AttachDisk(0, d)

	f.WriteData(0x42) // READ DIAGNOSTIC, MFM
	f.WriteData(0x00)
	f.WriteData(0x00)  // C
	f.WriteData(0x00)  // H
	f.WriteData(wantR) // R
	f.WriteData(0x02)  // N
	f.WriteData(0x01)  // EOT
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512 && f.phase == phaseExecution; i++ {
		f.ReadData()
	}
	return drainResult(t, f, 7)
}

// TestReadTrackSetsNoDataWhenTheIDIsNotOnTheTrack is the fault California
// Games hits.
func TestReadTrackSetsNoDataWhenTheIDIsNotOnTheTrack(t *testing.T) {
	d := trackWithIDs([]byte{0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7, 0xB8})

	res := readTrackResult(t, d, 0x01)

	if res[1]&st1ND == 0 {
		t.Errorf("ST1 = %02X, ND clear — READ A TRACK compares each sector's ID against the "+
			"one asked for, and R=01 is nowhere on a track numbered $B1-$B8", res[1])
	}
	if got := res[0] & (st0IC0 | st0IC1); got != st0IC0 {
		t.Errorf("ST0 = %02X, interrupt code %02X — want an abnormal termination", res[0], got)
	}
}

// The guard: when the track does carry the sector asked for, ND must stay
// clear. Without this the fix above could be "always set ND", which would
// break every legitimate READ TRACK.
func TestReadTrackLeavesNoDataClearWhenTheIDIsPresent(t *testing.T) {
	d := trackWithIDs([]byte{0x01, 0x02, 0x03})

	res := readTrackResult(t, d, 0x01)

	if res[1]&st1ND != 0 {
		t.Errorf("ST1 = %02X, ND set for a track that does carry R=01", res[1])
	}
}

// A protection track is read for its layout, not for one sector, so the result
// must still report end-of-cylinder alongside ND rather than replacing it.
func TestReadTrackStillReportsEndOfCylinderWithNoData(t *testing.T) {
	d := trackWithIDs([]byte{0xB1, 0xB2})

	res := readTrackResult(t, d, 0x01)

	if res[1]&st1EN == 0 {
		t.Errorf("ST1 = %02X, EN clear — the transfer still ran out its requested length", res[1])
	}
}
