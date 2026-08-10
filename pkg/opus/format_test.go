package opus

import (
	"bytes"
	"testing"
)

// WRITE TRACK is how the Opus formats a disk. The controller is handed a raw
// track image a byte at a time — gaps, sync, address marks, ID fields and
// sector data — exactly as it would be written to the medium, and it is the
// controller's job to pick the sectors out of that stream.
//
// The stream is not invented here. The ROM's format-data table (TAB_18_01,
// $1BDB) is a run-length list of (count, byte) pairs, and the placeholders
// $F0-$F4 are substituted with the real track, side, sector and size before
// the bytes are fed through the NMI handler at $189F:
//
//	DEFB +18, +4E    24 gap bytes
//	DEFB +0C, +00    12 sync zeros
//	DEFB +03, +F5    3 x A1 (F5 writes A1 with a missing clock)
//	DEFB +01, +FE    ID address mark
//	DEFB +01, +F1    (track)
//	DEFB +01, +F0    (side)
//	DEFB +01, +F4    (sector)
//	DEFB +01, +F2    (block-size)
//	DEFB +01, +F7    CRC
//	DEFB +16, +4E    22 gap bytes
//	DEFB +0C, +00    12 sync zeros
//	DEFB +03, +F5    3 x A1
//	DEFB +01, +FB    data address mark
//	DEFB +F3, +E5    the sector data, four runs of block-size/4
//	... x4
//	DEFB +01, +F7    CRC
//
// which is the standard IBM System 34 double-density format.

// romSectorStream builds one sector's worth of that table, with the
// placeholders substituted the way SETUP_2 does.
func romSectorStream(track, side, sector int, data []byte) []byte {
	var b bytes.Buffer
	rep := func(n int, v byte) {
		for i := 0; i < n; i++ {
			b.WriteByte(v)
		}
	}
	rep(24, 0x4E)
	rep(12, 0x00)
	rep(3, 0xA1)
	b.WriteByte(0xFE)
	b.WriteByte(byte(track))
	b.WriteByte(byte(side))
	b.WriteByte(byte(sector))
	b.WriteByte(0x01) // size code 1 = 128 << 1 = 256
	b.WriteByte(0xF7)
	rep(22, 0x4E)
	rep(12, 0x00)
	rep(3, 0xA1)
	b.WriteByte(0xFB)
	b.Write(data)
	b.WriteByte(0xF7)
	return b.Bytes()
}

// romTrackStream builds a whole track the way the ROM does: a leading gap,
// then every sector, then a trailing gap.
func romTrackStream(track int, fill byte) []byte {
	var b bytes.Buffer
	for i := 0; i < 11; i++ {
		b.WriteByte(0x4E) // TAB_18_00: DEFB +0B, +4E
	}
	data := bytes.Repeat([]byte{fill}, SectorSize)
	for sec := 0; sec < SectorsPerTrack; sec++ {
		b.Write(romSectorStream(track, 0, sec, data))
	}
	for b.Len() < TrackRawBytes {
		b.WriteByte(0x4E) // TAB_18_02: the trailing gap runs to the index pulse
	}
	return b.Bytes()
}

// feedTrack pushes a raw track through the data register the way NMI_FORMT
// does, one byte per DRQ.
func feedTrack(t *testing.T, d *Device, clk *clock, stream []byte) int {
	t.Helper()
	for i, v := range stream {
		if d.Read(FDCBase+0)&StatusBusy == 0 {
			return i // the index pulse ended the write
		}
		if d.Read(FDCBase+0)&StatusDRQ == 0 {
			t.Fatalf("byte %d: DRQ is not asserted during WRITE TRACK", i)
		}
		d.Write(FDCBase+3, v)
		clk.settle(d)
	}
	return len(stream)
}

// TestWriteTrackLaysDownEverySector is the core of formatting: after the
// stream has been fed, every sector of that track must be readable and hold
// the data the stream carried.
func TestWriteTrackLaysDownEverySector(t *testing.T) {
	img := NewImage()
	// Pre-fill the track with something else, so "formatted" is provable.
	for sec := 0; sec < SectorsPerTrack; sec++ {
		if err := img.WriteSector(5, sec, bytes.Repeat([]byte{0x77}, SectorSize)); err != nil {
			t.Fatal(err)
		}
	}

	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	clk := newClock(d)

	d.Write(FDCBase+1, 0x05) // the head is at track 5
	d.Write(FDCBase+0, 0xFC) // WRITE TRACK, the command FIND_ADDR builds for a format
	clk.settle(d)
	feedTrack(t, d, clk, romTrackStream(5, 0xE5))

	for sec := 0; sec < SectorsPerTrack; sec++ {
		got, err := img.ReadSector(5, sec)
		if err != nil {
			t.Fatalf("sector %d: %v", sec, err)
		}
		for i, b := range got {
			if b != 0xE5 {
				t.Fatalf("track 5 sector %d byte %d = %#02x, want the format filler 0xE5",
					sec, i, b)
			}
		}
	}
}

// TestWriteTrackLeavesOtherTracksAlone pins that a format writes only the
// track the head is over.
func TestWriteTrackLeavesOtherTracksAlone(t *testing.T) {
	img := NewImage()
	marker := bytes.Repeat([]byte{0x5A}, SectorSize)
	if err := img.WriteSector(9, 0, marker); err != nil {
		t.Fatal(err)
	}

	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	clk := newClock(d)
	d.Write(FDCBase+1, 0x05)
	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	feedTrack(t, d, clk, romTrackStream(5, 0xE5))

	got, err := img.ReadSector(9, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, marker) {
		t.Error("formatting track 5 modified track 9")
	}
}

// TestWriteTrackEndsAtTheIndexPulse pins the termination. A real WRITE TRACK
// runs from one index pulse to the next, so BUSY has to clear on its own after
// a track's worth of bytes — the ROM's WAIT_I/O loop
// ("LD A,(2800) / BIT 0,A / RET Z") has nothing else to wait for.
func TestWriteTrackEndsAtTheIndexPulse(t *testing.T) {
	d := New()
	d.Mount(0, NewImage())
	selectDrive(d, 0)
	clk := newClock(d)

	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	if d.Read(FDCBase+0)&StatusBusy == 0 {
		t.Fatal("BUSY is clear immediately after WRITE TRACK was issued")
	}

	// Feed far more than a track. The controller must stop on its own.
	stream := make([]byte, TrackRawBytes*2)
	for i := range stream {
		stream[i] = 0x4E
	}
	consumed := feedTrack(t, d, clk, stream)
	if consumed >= len(stream) {
		t.Fatalf("the controller accepted %d bytes without an index pulse", consumed)
	}
	if consumed != TrackRawBytes {
		t.Errorf("the track ended after %d bytes, want %d", consumed, TrackRawBytes)
	}
	if st := d.Read(FDCBase + 0); st&(StatusBusy|StatusDRQ) != 0 {
		t.Errorf("status = %#02x after the index pulse, want BUSY and DRQ clear", st)
	}
	// The ROM checks "AND +44" after a format: no write-protect, no lost data.
	if st := d.Read(FDCBase + 0); st&0x44 != 0 {
		t.Errorf("status = %#02x: a successful format must report no error", st)
	}
}

// TestWriteTrackHonoursWriteProtect pins the guard the ROM reports as
// "Write protected".
func TestWriteTrackHonoursWriteProtect(t *testing.T) {
	img := NewImage()
	marker := bytes.Repeat([]byte{0x33}, SectorSize)
	if err := img.WriteSector(0, 0, marker); err != nil {
		t.Fatal(err)
	}

	d := New()
	d.Mount(0, img)
	d.SetWriteProtect(0, true)
	selectDrive(d, 0)
	clk := newClock(d)

	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	st := d.Read(FDCBase + 0)
	if st&StatusWriteProtect == 0 {
		t.Errorf("status = %#02x, want WRITE PROTECT set", st)
	}
	if st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must not be left set", st)
	}
	got, err := img.ReadSector(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, marker) {
		t.Error("a write-protected disk was modified by a format")
	}
}

// TestWriteTrackWithNoDiskDoesNotHang pins the empty-drive path.
func TestWriteTrackWithNoDiskDoesNotHang(t *testing.T) {
	d := New()
	selectDrive(d, 0)
	clk := newClock(d)
	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	if st := d.Read(FDCBase + 0); st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must clear with no disk present", st)
	}
}

// TestWriteTrackNeedsSyncBeforeAnAddressMark guards the parser against
// false-triggering on sector data. $FE and $FB are only address marks when
// they follow the three $A1 sync bytes; the same values inside a data field
// are ordinary data.
func TestWriteTrackNeedsSyncBeforeAnAddressMark(t *testing.T) {
	img := NewImage()
	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	clk := newClock(d)

	// A sector whose data happens to contain $FE and $FB.
	data := make([]byte, SectorSize)
	for i := range data {
		data[i] = 0xE5
	}
	data[10], data[11], data[12] = 0xFE, 0x00, 0xFB

	var stream []byte
	stream = append(stream, romSectorStream(0, 0, 3, data)...)
	for len(stream) < TrackRawBytes {
		stream = append(stream, 0x4E)
	}

	d.Write(FDCBase+1, 0x00)
	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	feedTrack(t, d, clk, stream)

	got, err := img.ReadSector(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Error("the sector data was misparsed as address marks")
	}
}

// TestWriteTrackIgnoresSectorsOutsideTheGeometry pins the one thing a flat
// image cannot represent. A .opd holds 18 sectors per track and stores no ID
// fields, so a stream claiming sector 40 has nowhere to go; it must be
// dropped rather than corrupting a neighbour.
func TestWriteTrackIgnoresSectorsOutsideTheGeometry(t *testing.T) {
	img := NewImage()
	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	clk := newClock(d)

	data := bytes.Repeat([]byte{0xC3}, SectorSize)
	var stream []byte
	stream = append(stream, romSectorStream(0, 0, 40, data)...)
	stream = append(stream, romSectorStream(0, 0, 2, data)...)
	for len(stream) < TrackRawBytes {
		stream = append(stream, 0x4E)
	}

	d.Write(FDCBase+1, 0x00)
	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	feedTrack(t, d, clk, stream)

	// The in-range one landed.
	got, err := img.ReadSector(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Error("the in-range sector was not written")
	}
	// And nothing else on the track was touched.
	for sec := 0; sec < SectorsPerTrack; sec++ {
		if sec == 2 {
			continue
		}
		s, err := img.ReadSector(0, sec)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range s {
			if b != 0 {
				t.Fatalf("sector %d was modified by an out-of-range sector ID", sec)
			}
		}
	}
}

// TestFormatMarksTheImageModified pins that a formatted disk is offered for
// saving — otherwise the format would be lost on exit.
func TestFormatMarksTheImageModified(t *testing.T) {
	img := NewImage()
	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	clk := newClock(d)
	if img.Modified() {
		t.Fatal("precondition: a fresh image is unmodified")
	}
	d.Write(FDCBase+1, 0x00)
	d.Write(FDCBase+0, 0xFC)
	clk.settle(d)
	feedTrack(t, d, clk, romTrackStream(0, 0xE5))
	if !img.Modified() {
		t.Error("formatting did not mark the image modified")
	}
}
