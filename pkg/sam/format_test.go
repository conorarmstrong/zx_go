package sam

import (
	"bytes"
	"testing"
)

func blankMGTDisk(t *testing.T) *Disk {
	t.Helper()
	d, err := LoadDisk(make([]byte, mgt800KSize))
	if err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}
	return d
}

// streamWriteTrack drives a WRITE TRACK ($F0) with a format stream that
// lays down `sectors` records on cylinder cyl, each filled with fill.
func streamWriteTrack(f *WD1772, cyl, sectors int, fill byte) {
	var stream []byte
	for s := 1; s <= sectors; s++ {
		stream = append(stream, 0x4E, 0x4E)                           // gap
		stream = append(stream, 0xFE, byte(cyl), 0x00, byte(s), 0x02) // IDAM: C,H,R,N (N=2 → 512)
		stream = append(stream, 0xF7)                                 // write CRC
		stream = append(stream, 0x4E, 0x4E)                           // gap
		stream = append(stream, 0xFB)                                 // data address mark
		stream = append(stream, bytes.Repeat([]byte{fill}, 512)...)   // sector data
		stream = append(stream, 0xF7)                                 // write CRC
	}
	// A real format streams a whole track; pad to the full length with
	// gap bytes so the controller sees the transfer complete.
	for len(stream) < writeTrackLen {
		stream = append(stream, 0x4E)
	}
	f.WriteCommand(0xF0)
	for _, b := range stream[:writeTrackLen] {
		f.WriteData(b)
	}
}

// FORMAT / WRITE TRACK used to fall into the "complete benignly" branch:
// INTRQ raised, no error reported and not one byte moved. SAMDOS said the
// disk was formatted and every old sector was still there.
func TestWriteTrackFormatsTheTrack(t *testing.T) {
	d := blankMGTDisk(t)
	// Put recognisable old content on the track we are about to format.
	old := bytes.Repeat([]byte{0xAA}, 512)
	for s := 1; s <= 10; s++ {
		if !d.WriteSector(3, 0, s, old) {
			t.Fatalf("setup: WriteSector %d failed", s)
		}
	}

	f := NewWD1772()
	f.InsertDisk(d)
	f.WriteCommand(0x1B) // SEEK is driven from the data register
	f.SetSide(0)
	f.seekTo(3)

	streamWriteTrack(f, 3, 10, 0xE5)

	if f.status&wdBusy != 0 {
		t.Errorf("controller still BUSY after the stream: status %#02x", f.status)
	}
	for s := 1; s <= 10; s++ {
		got, ok := d.ReadSector(3, 0, s)
		if !ok {
			t.Fatalf("ReadSector %d: not found", s)
		}
		if !bytes.Equal(got, bytes.Repeat([]byte{0xE5}, 512)) {
			t.Errorf("sector %d after format: first bytes % x, want e5...", s, got[:8])
		}
	}
}

// A format must not touch a neighbouring track.
func TestWriteTrackLeavesOtherTracksAlone(t *testing.T) {
	d := blankMGTDisk(t)
	keep := bytes.Repeat([]byte{0x5A}, 512)
	if !d.WriteSector(4, 0, 1, keep) {
		t.Fatal("setup: WriteSector failed")
	}

	f := NewWD1772()
	f.InsertDisk(d)
	f.seekTo(3)
	streamWriteTrack(f, 3, 10, 0xE5)

	got, ok := d.ReadSector(4, 0, 1)
	if !ok || !bytes.Equal(got, keep) {
		t.Errorf("cylinder 4 sector 1 changed: % x", got[:8])
	}
}

// A write-protected disk must refuse the format rather than report success.
func TestWriteTrackRefusesAWriteProtectedDisk(t *testing.T) {
	d := blankMGTDisk(t)
	d.SetWriteProtect(true)

	f := NewWD1772()
	f.InsertDisk(d)
	f.WriteCommand(0xF0)
	if f.status&wdWriteProt == 0 {
		t.Errorf("status %#02x, want the write-protect bit set", f.status)
	}
}

// With no disk in the drive, WRITE TRACK is a record-not-found.
func TestWriteTrackWithNoDiskIsRecordNotFound(t *testing.T) {
	f := NewWD1772()
	f.WriteCommand(0xF0)
	if f.status&wdRNF == 0 {
		t.Errorf("status %#02x, want RNF", f.status)
	}
}

// READ TRACK returns a track image the host can parse back: the same
// IDAM/DAM framing a format writes.
func TestReadTrackReturnsAParsableTrackImage(t *testing.T) {
	d := blankMGTDisk(t)
	payload := bytes.Repeat([]byte{0x3C}, 512)
	if !d.WriteSector(2, 0, 1, payload) {
		t.Fatal("setup: WriteSector failed")
	}

	f := NewWD1772()
	f.InsertDisk(d)
	f.seekTo(2)
	f.WriteCommand(0xE0)
	if f.status&wdDRQ == 0 {
		t.Fatalf("READ TRACK did not raise DRQ: status %#02x", f.status)
	}
	var image []byte
	for f.status&wdDRQ != 0 && len(image) < 32768 {
		image = append(image, f.ReadData())
	}
	// The first sector's ID must be in there, followed by its data.
	idx := bytes.Index(image, []byte{0xFE, 0x02, 0x00, 0x01, 0x02})
	if idx < 0 {
		t.Fatalf("no IDAM for C=2 H=0 R=1 in the track image (%d bytes)", len(image))
	}
	if !bytes.Contains(image, payload) {
		t.Error("sector 1's data is not in the track image")
	}
}

// A host that stops the stream early — a FORCE INTERRUPT mid-format —
// keeps whatever already streamed past the head, as on the real part.
func TestForceInterruptCommitsAPartialFormat(t *testing.T) {
	d := blankMGTDisk(t)
	f := NewWD1772()
	f.InsertDisk(d)
	f.seekTo(6)

	f.WriteCommand(0xF0)
	var stream []byte
	stream = append(stream, 0xFE, 0x06, 0x00, 0x01, 0x02, 0xF7, 0xFB)
	stream = append(stream, bytes.Repeat([]byte{0x77}, 512)...)
	for _, b := range stream {
		f.WriteData(b)
	}
	f.WriteCommand(0xD0) // FORCE INTERRUPT

	got, ok := d.ReadSector(6, 0, 1)
	if !ok {
		t.Fatal("ReadSector: not found")
	}
	if !bytes.Equal(got, bytes.Repeat([]byte{0x77}, 512)) {
		t.Errorf("sector 1 after aborted format: % x, want 77...", got[:8])
	}
}

// A new command ends a format in progress, and the commit must not raise
// INTRQ for the command that just started: a host polling the line
// between the command write and the first DRQ would read "finished" and
// abandon the transfer.
func TestANewCommandAfterAFormatDoesNotSignalCompletion(t *testing.T) {
	d := blankMGTDisk(t)
	f := NewWD1772()
	f.InsertDisk(d)
	f.seekTo(2)

	f.WriteCommand(0xF0)
	f.WriteData(0xFE)

	f.WriteSector(1)
	f.WriteCommand(0x80) // READ SECTOR
	if f.intrq {
		t.Error("INTRQ raised by the command that just started")
	}
	if f.status&wdDRQ == 0 {
		t.Errorf("READ SECTOR did not raise DRQ: status %#02x", f.status)
	}
}

// FORCE INTERRUPT is the one command that should end with INTRQ raised,
// because that is what it is for.
func TestForceInterruptAfterAFormatStillSignals(t *testing.T) {
	d := blankMGTDisk(t)
	f := NewWD1772()
	f.InsertDisk(d)
	f.seekTo(2)

	f.WriteCommand(0xF0)
	f.WriteData(0xFE)
	f.WriteCommand(0xD0)
	if !f.intrq {
		t.Error("FORCE INTERRUPT did not raise INTRQ")
	}
}
