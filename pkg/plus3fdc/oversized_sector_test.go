package plus3fdc

import "testing"

// edskCRCFlagged builds a one-sector EDSK track carrying the status bytes
// Captain Planet, Barbarian II, Chase HQ II, Back to the Future II and
// Comando Quatro all record on their protected tracks: ST1=0x20 (DE, a CRC
// error occurred) with ST2=0x60 (CM, deleted mark, plus DD, the error was in
// the data field).
func edskCRCFlagged(st1, st2 byte) []byte {
	const trackLen = 6656
	data := make([]byte, dskHeaderSize)
	copy(data, "EXTENDED CPC DSK File\r\nDisk-Info\r\n")
	data[0x30] = 1
	data[0x31] = 1
	data[0x34] = byte(trackLen / 256)

	tr := make([]byte, trackLen)
	copy(tr, trackInfoSignature)
	tr[0x10] = 1 // C
	tr[0x14] = 6 // N
	tr[0x15] = 1 // one sector
	tr[0x17] = 0xE5

	b := sectorInfoOffset
	tr[b+0] = 1    // C
	tr[b+2] = 0xC1 // R
	tr[b+3] = 6    // N
	tr[b+4] = st1
	tr[b+5] = st2
	tr[b+6] = byte(6144 & 0xFF)
	tr[b+7] = byte(6144 >> 8)
	return append(data, tr...)
}

// ST1.DE reports that a CRC error occurred; ST2.DD reports that it was in the
// data field. DE with DD set therefore describes an intact ID and a bad data
// field, and the sector must still be findable. Reading every DE as an ID
// error made these sectors invisible to READ DATA while READ ID kept
// returning them, which is what left five +3 titles on a black screen.
func TestDataFieldCRCErrorLeavesTheIDIntact(t *testing.T) {
	d, err := ParseDSK(edskCRCFlagged(0x20, 0x60))
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	tr := d.Tracks[0][0]
	if tr == nil {
		t.Fatal("track not built")
	}
	_, _, r, n, _, crcOK, ok := tr.idAtCRC(0)
	if !ok {
		t.Fatal("no IDAM on the track")
	}
	if r != 0xC1 || n != 6 {
		t.Fatalf("IDAM R=%02X N=%02X, want C1/06", r, n)
	}
	if !crcOK {
		t.Error("ID CRC marked bad, but ST2.DD says the error was in the data field")
	}
}

// The other direction must still hold: DE without DD is a genuine ID-field
// CRC error and must stay marked as one.
func TestIDFieldCRCErrorIsStillHonoured(t *testing.T) {
	d, err := ParseDSK(edskCRCFlagged(0x20, 0x00))
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	_, _, _, _, _, crcOK, ok := d.Tracks[0][0].idAtCRC(0)
	if !ok {
		t.Fatal("no IDAM on the track")
	}
	if crcOK {
		t.Error("ID CRC marked good, but ST1.DE without ST2.DD is an ID-field error")
	}
}

// speedlockTrack builds the track layout Captain Planet, Barbarian II,
// Chase HQ II, Back to the Future II and Adidas Tie-Break all use: one
// sector whose ID declares N=6 (a nominal 8192 bytes) while the track
// physically holds only 6144 bytes of data for it.
//
// The declared size deliberately exceeds what fits in a revolution. That is
// the whole point of the scheme — a copier reading the sector cannot get a
// clean transfer — and a real µPD765 has no idea it is impossible. It reads
// the ID, streams the number of bytes N asks for straight off the surface,
// crosses the index hole and keeps going, then fails the CRC at the end.
func speedlockTrack(r byte) *Disk {
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(6476, 0xE5, 1, 0, 6)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(1, 0, r, 6, false)
	b.dataAdd(make([]byte, 6144), false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr
	return d
}

// An oversized sector must transfer its data. Refusing it is what left five
// +3 titles on a black screen: READ ID reported the sector happily while
// READ DATA answered "no data" for the very same one.
func TestReadOversizedSectorTransfersData(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, speedlockTrack(0xC1))

	f.WriteData(0x46) // READ DATA (MFM)
	f.WriteData(0x00)
	f.WriteData(0x01) // C
	f.WriteData(0x00) // H
	f.WriteData(0xC1) // R
	f.WriteData(0x06) // N = 6, a nominal 8192 bytes
	f.WriteData(0xC1) // EOT
	f.WriteData(0x2A)
	f.WriteData(0xFF)

	// The controller must be in execution phase with data to give, not
	// sitting in a result phase having refused the command.
	if got := f.ReadStatus() & (msrCB | msrEXM | msrDIO); got != msrCB|msrEXM|msrDIO {
		res := drainResult(t, f, 7)
		t.Fatalf("refused the sector: ST0=%02X ST1=%02X ST2=%02X, want an execution phase",
			res[0], res[1], res[2])
	}

	// All 6144 stored bytes must come back before anything else.
	for i := 0; i < 6144; i++ {
		if got := f.ReadData(); got != 0x00 {
			t.Fatalf("byte %d = %02X, want 00 — stored data was not returned", i, got)
		}
	}
}

// READ ID and READ DATA must agree. A controller that finds a sector with one
// command and denies it exists with the next is wrong whatever else it does.
func TestReadIDAndReadDataAgreeOnOversizedSector(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, speedlockTrack(0xC1))

	f.WriteData(0x4A) // READ ID
	f.WriteData(0x00)
	id := drainResult(t, f, 7)
	if id[0]&st0IC0 != 0 {
		t.Fatalf("READ ID failed: ST0=%02X", id[0])
	}
	if id[5] != 0xC1 || id[6] != 0x06 {
		t.Fatalf("READ ID returned R=%02X N=%02X, want C1/06", id[5], id[6])
	}

	f.WriteData(0x4C) // READ DELETED DATA, as the real loaders use
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x00)
	f.WriteData(id[5])
	f.WriteData(id[6])
	f.WriteData(id[5])
	f.WriteData(0x2A)
	f.WriteData(0xFF)

	if f.phase != phaseExecution {
		res := drainResult(t, f, 7)
		t.Fatalf("READ ID found R=%02X but READ DATA returned ST0=%02X ST1=%02X",
			id[5], res[0], res[1])
	}
}

// The head crosses the index hole; it does not stop at it. Reading past the
// end of a revolution must wrap to the start of the same track.
func TestTrackReadWrapsAtTheIndexHole(t *testing.T) {
	tr := newTrack(256, 0xE5, 0, 0, 2)
	tr.data[0] = 0xAA
	tr.data[1] = 0xBB
	if got := tr.readByte(256); got != 0xAA {
		t.Errorf("readByte(bpt) = %02X, want %02X — did not wrap", got, 0xAA)
	}
	if got := tr.readByte(257); got != 0xBB {
		t.Errorf("readByte(bpt+1) = %02X, want %02X — did not wrap", got, 0xBB)
	}
}

// The µPD765 matches a sector on the whole ID — C, H, R and N — not on the
// sector number alone. A command asking for N=2 must not be satisfied by a
// sector whose ID declares N=0, however well R matches.
//
// This is load-bearing for copy protection. Action Force puts a 128-byte
// sector (N=0) at R=79 on track 0 and its loader reads that sector asking for
// N=2; the read is supposed to fail. Matching on R alone handed the loader a
// short sector plus a data-CRC error instead of "no data", and the game
// dropped back to BASIC. A +3 reference draws its title screen.
func TestSectorMatchRequiresTheSizeCodeToo(t *testing.T) {
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 0x79, 0, false) // R=79 with N=0, a 128-byte sector
	b.dataAdd(make([]byte, 128), false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)

	f.WriteData(0x4C) // READ DELETED DATA
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x79) // R matches
	f.WriteData(0x02) // N does not — the sector on the track is N=0
	f.WriteData(0x79)
	f.WriteData(0x2A)
	f.WriteData(0xFF)

	if f.phase == phaseExecution {
		t.Fatal("N=2 request matched an N=0 sector; the ID compare ignored the size code")
	}
	res := drainResult(t, f, 7)
	if res[1]&st1ND == 0 {
		t.Errorf("ST1 = %02X, want ND set — no sector matches the requested ID", res[1])
	}
}

// The matching size code must still be found, on the same track.
func TestSectorMatchAcceptsTheDeclaredSizeCode(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, speedlockTrack(0xC1))

	f.WriteData(0x4C)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x00)
	f.WriteData(0xC1)
	f.WriteData(0x06) // matches the ID's N
	f.WriteData(0xC1)
	f.WriteData(0x2A)
	f.WriteData(0xFF)

	if f.phase != phaseExecution {
		res := drainResult(t, f, 7)
		t.Fatalf("matching N=6 request refused: ST0=%02X ST1=%02X", res[0], res[1])
	}
}
