package plus3fdc

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildSyntheticDSK constructs a minimal but valid DSK image in memory.
// The geometry mirrors a typical +3 system disk: 2 sides, 40 cylinders,
// 9 sectors per track, 512 bytes per sector. Each sector is filled with a
// recognisable pattern (cylinder, head, sector ID, then 0xCC repeated)
// so tests can verify the parser unpacked everything in the right order.
func buildSyntheticDSK(t *testing.T, extended bool) ([]byte, dskExpected) {
	t.Helper()

	const (
		sides         = 2
		cylinders     = 40
		sectorsPerTrk = 9
		sectorN       = byte(2) // 512 bytes
		sectorSize    = 128 << sectorN
		trackDataSize = sectorsPerTrk * sectorSize
		trackTotal    = trackHeaderSize + trackDataSize
	)

	hdr := make([]byte, dskHeaderSize)
	if extended {
		copy(hdr[0:], []byte(dskSignatureExt))
	} else {
		copy(hdr[0:], []byte(dskSignatureStd))
	}
	copy(hdr[0x22:], []byte("zx_go test    ")) // 14 bytes creator
	hdr[0x30] = cylinders
	hdr[0x31] = sides
	if !extended {
		binary.LittleEndian.PutUint16(hdr[0x32:0x34], uint16(trackTotal))
	} else {
		// Per-track length table: trackTotal/256 for every track.
		for i := 0; i < sides*cylinders; i++ {
			hdr[0x34+i] = byte(trackTotal / 256)
		}
	}

	var buf bytes.Buffer
	buf.Write(hdr)

	expected := dskExpected{
		sides:     sides,
		cylinders: cylinders,
		sectors:   make(map[chrnKey][]byte),
	}

	// Tracks are written in (cylinder, head) order — i.e. cyl 0 head 0,
	// cyl 0 head 1, cyl 1 head 0, ... This is the same order CPCEMU uses.
	for cyl := 0; cyl < cylinders; cyl++ {
		for head := 0; head < sides; head++ {
			track := make([]byte, trackTotal)
			copy(track[0:], []byte(trackInfoSignature))
			track[0x10] = byte(cyl)
			track[0x11] = byte(head)
			track[0x14] = sectorN
			track[0x15] = sectorsPerTrk
			track[0x16] = 0x4E // GAP3 length, ignored by parser
			track[0x17] = 0xE5 // Filler byte

			// Sector info table — sector IDs 1..9 in order.
			for j := 0; j < sectorsPerTrk; j++ {
				base := sectorInfoOffset + j*sectorInfoSize
				track[base+0] = byte(cyl)
				track[base+1] = byte(head)
				track[base+2] = byte(j + 1) // R: 1-based
				track[base+3] = sectorN
				if extended {
					binary.LittleEndian.PutUint16(track[base+6:base+8], sectorSize)
				}
			}

			// Sector data — distinguishable per sector.
			for j := 0; j < sectorsPerTrk; j++ {
				dst := track[trackHeaderSize+j*sectorSize : trackHeaderSize+(j+1)*sectorSize]
				dst[0] = byte(cyl)
				dst[1] = byte(head)
				dst[2] = byte(j + 1)
				for k := 3; k < sectorSize; k++ {
					dst[k] = 0xCC
				}

				key := chrnKey{C: byte(cyl), H: byte(head), R: byte(j + 1)}
				expected.sectors[key] = append([]byte(nil), dst...)
			}

			buf.Write(track)
		}
	}

	return buf.Bytes(), expected
}

type chrnKey struct {
	C, H, R byte
}

type dskExpected struct {
	sides     int
	cylinders int
	sectors   map[chrnKey][]byte
}

func (e dskExpected) verify(t *testing.T, d *Disk) {
	t.Helper()
	if d.Sides != e.sides {
		t.Errorf("sides: got %d want %d", d.Sides, e.sides)
	}
	if d.Cylinders != e.cylinders {
		t.Errorf("cylinders: got %d want %d", d.Cylinders, e.cylinders)
	}
	for key, want := range e.sectors {
		track := d.Track(int(key.H), int(key.C))
		if track == nil {
			t.Errorf("track h=%d c=%d missing", key.H, key.C)
			continue
		}
		s := track.FindSector(key.R)
		if s == nil {
			t.Errorf("h=%d c=%d r=%d not found in track", key.H, key.C, key.R)
			continue
		}
		if !bytes.Equal(s.Data, want) {
			t.Errorf("h=%d c=%d r=%d data mismatch: first 4 bytes got %v want %v",
				key.H, key.C, key.R, s.Data[:4], want[:4])
		}
	}
}

func TestParseDSKStandard(t *testing.T) {
	data, expected := buildSyntheticDSK(t, false)
	d, err := ParseDSK(data)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	expected.verify(t, d)
}

func TestParseDSKExtended(t *testing.T) {
	data, expected := buildSyntheticDSK(t, true)
	d, err := ParseDSK(data)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	expected.verify(t, d)
}

func TestParseDSKBadSignature(t *testing.T) {
	data := make([]byte, dskHeaderSize)
	copy(data, []byte("not a DSK file at all"))
	if _, err := ParseDSK(data); err == nil {
		t.Fatal("expected error for bad signature, got nil")
	}
}

func TestParseDSKTooShort(t *testing.T) {
	if _, err := ParseDSK([]byte{0, 1, 2}); err == nil {
		t.Fatal("expected error for short file, got nil")
	}
}

func TestSaveDSKRoundTrip(t *testing.T) {
	// Build a synthetic disk, save it, reload, verify all sectors
	// round-trip with the same data.
	original, expected := buildSyntheticDSK(t, false)
	d1, err := ParseDSK(original)
	if err != nil {
		t.Fatalf("ParseDSK original: %v", err)
	}
	saved, err := d1.SerializeDSK()
	if err != nil {
		t.Fatalf("SerializeDSK: %v", err)
	}
	d2, err := ParseDSK(saved)
	if err != nil {
		t.Fatalf("ParseDSK reloaded: %v", err)
	}
	expected.verify(t, d2)
}

func TestSaveDSKAfterSectorWrite(t *testing.T) {
	// Mutate a sector via the FDC write path, then save the disk and
	// verify the reloaded disk contains the modified data — this is
	// the full "edit, save, reopen" workflow.
	syn, _ := buildSyntheticDSK(t, false)
	d, err := ParseDSK(syn)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	f := NewUPD765()
	f.AttachDisk(0, d)

	// Overwrite sector 3 of track 2, head 0 with a known pattern.
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(2)
	f.WriteData(0x08)
	drainResult(t, f, 2)

	f.WriteData(0x45)
	f.WriteData(0x00)
	f.WriteData(0x02) // C
	f.WriteData(0x00) // H
	f.WriteData(0x03) // R
	f.WriteData(0x02)
	f.WriteData(0x03)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	want := make([]byte, 512)
	for i := range want {
		want[i] = byte(0x90 + i%7)
	}
	for _, by := range want {
		f.WriteData(by)
	}
	drainResult(t, f, 7)

	// Save → reload → inspect sector 3 of (h=0, c=2).
	saved, err := d.SerializeDSK()
	if err != nil {
		t.Fatalf("SerializeDSK: %v", err)
	}
	d2, err := ParseDSK(saved)
	if err != nil {
		t.Fatalf("ParseDSK reloaded: %v", err)
	}
	track := d2.Track(0, 2)
	if track == nil {
		t.Fatal("reloaded track 0/2 missing")
	}
	s := track.FindSector(3)
	if s == nil {
		t.Fatal("sector R=3 missing after reload")
	}
	for i := range want {
		if s.Data[i] != want[i] {
			t.Errorf("byte %d: got %02X, want %02X", i, s.Data[i], want[i])
			break
		}
	}
}

func TestSaveDSKPreservesIDAMOnlySector(t *testing.T) {
	// Build a track with an IDAM that has no following DAM (a header
	// without a data field, used by some copy-protection schemes).
	// Save → reload → READ DATA should report ST1.MA / ST1.ND, not
	// silently return the bytes that physically follow the IDAM.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	// Sector 1 has IDAM only — no dataAdd call.
	if !b.idAdd(0, 0, 1, 2, false) {
		t.Fatal("idAdd failed")
	}
	// Sector 2 is a normal sector for comparison.
	b.idAdd(0, 0, 2, 2, false)
	want := make([]byte, 512)
	for i := range want {
		want[i] = 0xA5
	}
	b.dataAdd(want, false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	// Save and reload.
	saved, err := d.SerializeDSK()
	if err != nil {
		t.Fatalf("SerializeDSK: %v", err)
	}
	d2, err := ParseDSK(saved)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}

	// Sector 1 should fail with ST1.MA / ST1.ND.
	f := NewUPD765()
	f.AttachDisk(0, d2)
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("IDAM-only sector after reload: ST0=%02X, expected abnormal", res[0])
	}
	if res[1]&(st1MA|st1ND) == 0 {
		t.Errorf("IDAM-only sector after reload: ST1=%02X, expected MA or ND", res[1])
	}

	// Sector 2 should still read cleanly.
	f.headPos[0] = 0
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x02)
	f.WriteData(0x02)
	f.WriteData(0x02)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	got := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		got = append(got, f.ReadData())
	}
	res = drainResult(t, f, 7)
	if res[0]&st0IC0 != 0 {
		t.Errorf("normal sector after reload: ST0=%02X, expected normal", res[0])
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normal sector byte %d: got %02X, want %02X", i, got[i], want[i])
			break
		}
	}
}

func TestSaveDSKPreservesDeletedDAM(t *testing.T) {
	// A sector with a deleted DAM should round-trip through save/load
	// with ST2.CM still set on the next read.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), true, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	saved, err := d.SerializeDSK()
	if err != nil {
		t.Fatalf("SerializeDSK: %v", err)
	}
	d2, err := ParseDSK(saved)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}

	f := NewUPD765()
	f.AttachDisk(0, d2)
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x40 == 0 {
		t.Errorf("reloaded deleted sector: ST2=%02X, CM clear, want set", res[2])
	}
}

func TestParseDSKMissingTrack(t *testing.T) {
	// Build an extended DSK with one track marked absent (length byte 0).
	data, expected := buildSyntheticDSK(t, true)
	// Remove cylinder 5 head 0 by zeroing its track length byte and
	// physically excising the track from the file.
	// File order is (cyl, head): 0 0, 0 1, 1 0, 1 1, ..., 5 0, 5 1, ...
	// Index of cyl 5 head 0 is 5*2+0 = 10.
	idx := 10
	data[0x34+idx] = 0
	// Walk forward to that track and excise it.
	const trackTotal = trackHeaderSize + 9*512
	cutStart := dskHeaderSize + idx*trackTotal
	cutEnd := cutStart + trackTotal
	excised := append([]byte(nil), data[:cutStart]...)
	excised = append(excised, data[cutEnd:]...)

	d, err := ParseDSK(excised)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	if d.Track(0, 5) != nil {
		t.Errorf("expected cyl 5 head 0 to be absent, got non-nil track")
	}
	// All other sectors should still be present.
	for key, want := range expected.sectors {
		if key.H == 0 && key.C == 5 {
			continue
		}
		track := d.Track(int(key.H), int(key.C))
		if track == nil {
			t.Errorf("track h=%d c=%d missing after excision", key.H, key.C)
			continue
		}
		s := track.FindSector(key.R)
		if s == nil {
			t.Errorf("h=%d c=%d r=%d missing after excision", key.H, key.C, key.R)
			continue
		}
		if !bytes.Equal(s.Data, want) {
			t.Errorf("h=%d c=%d r=%d data mismatch", key.H, key.C, key.R)
		}
	}
}
