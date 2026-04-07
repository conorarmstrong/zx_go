package plus3fdc

import (
	"bytes"
	"testing"
)

// buildRawMGT constructs a minimal MGT image in memory. The image is
// a 2-side, 80-cylinder, 10 sectors × 512 byte DSDD disk; each sector
// is filled with a recognisable pattern so tests can verify the
// loader returned the right bytes to the right C/H/R.
func buildRawMGT(t *testing.T, altSides bool) []byte {
	t.Helper()
	const (
		sides        = 2
		cylinders    = 80
		sectors      = 10
		sectorSize   = 512
		totalSectors = sides * cylinders * sectors
	)
	out := make([]byte, totalSectors*sectorSize)

	fillSector := func(buf []byte, cyl, head, r byte) {
		buf[0] = cyl
		buf[1] = head
		buf[2] = r
		for i := 3; i < len(buf); i++ {
			buf[i] = 0xAA
		}
	}

	pos := 0
	if altSides {
		for c := 0; c < cylinders; c++ {
			for h := 0; h < sides; h++ {
				for r := 0; r < sectors; r++ {
					fillSector(out[pos:pos+sectorSize], byte(c), byte(h), byte(r+1))
					pos += sectorSize
				}
			}
		}
	} else {
		for h := 0; h < sides; h++ {
			for c := 0; c < cylinders; c++ {
				for r := 0; r < sectors; r++ {
					fillSector(out[pos:pos+sectorSize], byte(c), byte(h), byte(r+1))
					pos += sectorSize
				}
			}
		}
	}
	return out
}

func TestParseMGT(t *testing.T) {
	data := buildRawMGT(t, true)
	d, err := ParseMGT(data)
	if err != nil {
		t.Fatalf("ParseMGT: %v", err)
	}
	if d.Sides != 2 || d.Cylinders != 80 {
		t.Errorf("geometry: sides=%d cylinders=%d, want 2/80", d.Sides, d.Cylinders)
	}
	// Spot-check: cyl 10 head 1 sector R=3 should encode (10, 1, 3).
	tr := d.Track(1, 10)
	if tr == nil {
		t.Fatal("track h=1 c=10 missing")
	}
	s := tr.FindSector(3)
	if s == nil {
		t.Fatal("sector R=3 missing")
	}
	if s.Data[0] != 10 || s.Data[1] != 1 || s.Data[2] != 3 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (10,1,3)",
			s.Data[0], s.Data[1], s.Data[2])
	}
}

func TestParseIMG(t *testing.T) {
	data := buildRawMGT(t, false) // out-out order
	d, err := ParseIMG(data)
	if err != nil {
		t.Fatalf("ParseIMG: %v", err)
	}
	tr := d.Track(1, 10)
	if tr == nil {
		t.Fatal("track h=1 c=10 missing")
	}
	s := tr.FindSector(3)
	if s == nil {
		t.Fatal("sector R=3 missing")
	}
	if s.Data[0] != 10 || s.Data[1] != 1 || s.Data[2] != 3 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (10,1,3)",
			s.Data[0], s.Data[1], s.Data[2])
	}
}

func TestParseTRD(t *testing.T) {
	// Build a 2×80×16×256 TRD with tagged sectors.
	const (
		sides      = 2
		cylinders  = 80
		sectors    = 16
		sectorSize = 256
	)
	out := make([]byte, sides*cylinders*sectors*sectorSize)
	pos := 0
	for c := 0; c < cylinders; c++ {
		for h := 0; h < sides; h++ {
			for r := 0; r < sectors; r++ {
				out[pos+0] = byte(c)
				out[pos+1] = byte(h)
				out[pos+2] = byte(r + 1)
				pos += sectorSize
			}
		}
	}
	d, err := ParseTRD(out)
	if err != nil {
		t.Fatalf("ParseTRD: %v", err)
	}
	if d.Sides != 2 || d.Cylinders != 80 {
		t.Errorf("TRD geometry: sides=%d cylinders=%d, want 2/80", d.Sides, d.Cylinders)
	}
	// Spot-check cyl 33 head 1 sector 7.
	tr := d.Track(1, 33)
	if tr == nil {
		t.Fatal("track h=1 c=33 missing")
	}
	s := tr.FindSector(7)
	if s == nil {
		t.Fatal("sector R=7 missing")
	}
	if s.Data[0] != 33 || s.Data[1] != 1 || s.Data[2] != 7 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (33,1,7)",
			s.Data[0], s.Data[1], s.Data[2])
	}
}

func TestParseMGTRejectsBadSize(t *testing.T) {
	if _, err := ParseMGT(make([]byte, 12345)); err == nil {
		t.Fatal("expected error for bogus MGT size")
	}
}

func TestParseTRDRejectsNonAligned(t *testing.T) {
	if _, err := ParseTRD(make([]byte, 5000)); err == nil {
		t.Fatal("expected error for non-aligned TRD size")
	}
}

func TestParseSAD(t *testing.T) {
	const (
		sides      = 2
		cylinders  = 40
		sectors    = 10
		sectorSize = 512
	)
	hdr := make([]byte, 22)
	copy(hdr, sadSignature)
	hdr[18] = byte(sides)
	hdr[19] = byte(cylinders)
	hdr[20] = byte(sectors)
	hdr[21] = byte(sectorSize / 64)

	data := make([]byte, 22+sides*cylinders*sectors*sectorSize)
	copy(data, hdr)
	pos := 22
	// Out-out order: all of side 0 first.
	for h := 0; h < sides; h++ {
		for c := 0; c < cylinders; c++ {
			for r := 0; r < sectors; r++ {
				data[pos+0] = byte(c)
				data[pos+1] = byte(h)
				data[pos+2] = byte(r + 1)
				pos += sectorSize
			}
		}
	}
	d, err := ParseSAD(data)
	if err != nil {
		t.Fatalf("ParseSAD: %v", err)
	}
	tr := d.Track(1, 7)
	if tr == nil {
		t.Fatal("track h=1 c=7 missing")
	}
	s := tr.FindSector(5)
	if s == nil {
		t.Fatal("sector R=5 missing")
	}
	if s.Data[0] != 7 || s.Data[1] != 1 || s.Data[2] != 5 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (7,1,5)",
			s.Data[0], s.Data[1], s.Data[2])
	}
}

func TestParseSADRejectsWrongSignature(t *testing.T) {
	data := make([]byte, 22)
	copy(data, []byte("not a SAD file    "))
	if _, err := ParseSAD(data); err == nil {
		t.Fatal("expected signature error")
	}
}

func TestParseD40D80(t *testing.T) {
	const (
		sides      = 2
		cylinders  = 80
		sectors    = 10
		sectorSize = 512
	)
	data := make([]byte, sides*cylinders*sectors*sectorSize)
	// Put geometry at fixed offsets within the first track.
	data[0xB1] = 0x10 // bit 4 set → 2 sides
	data[0xB2] = cylinders
	data[0xB3] = sectors
	pos := 0
	for c := 0; c < cylinders; c++ {
		for h := 0; h < sides; h++ {
			for r := 0; r < sectors; r++ {
				data[pos+0] = byte(c)
				data[pos+1] = byte(h)
				data[pos+2] = byte(r + 1)
				pos += sectorSize
			}
		}
	}
	d, err := ParseD40D80(data)
	if err != nil {
		t.Fatalf("ParseD40D80: %v", err)
	}
	if d.Sides != 2 || d.Cylinders != 80 {
		t.Errorf("Didaktik geometry: sides=%d cyls=%d, want 2/80", d.Sides, d.Cylinders)
	}
	tr := d.Track(1, 20)
	if tr == nil {
		t.Fatal("track h=1 c=20 missing")
	}
	s := tr.FindSector(4)
	if s == nil {
		t.Fatal("sector R=4 missing")
	}
	if s.Data[0] != 20 || s.Data[1] != 1 || s.Data[2] != 4 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (20,1,4)",
			s.Data[0], s.Data[1], s.Data[2])
	}
}

func TestRawTrackRoundTripsThroughFDC(t *testing.T) {
	// Build an MGT, load it, mount on the FDC, READ DATA, verify data.
	data := buildRawMGT(t, true)
	d, err := ParseMGT(data)
	if err != nil {
		t.Fatalf("ParseMGT: %v", err)
	}
	f := NewUPD765()
	f.AttachDisk(0, d)
	// SEEK to cyl 5.
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(5)
	f.WriteData(0x08)
	drainResult(t, f, 2)
	// READ DATA R=2 (512 bytes).
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x05)
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
	drainResult(t, f, 7)
	if got[0] != 5 || got[1] != 0 || got[2] != 2 {
		t.Errorf("sector prefix = (%d,%d,%d), want (5,0,2)", got[0], got[1], got[2])
	}
	tail := got[3:]
	want := bytes.Repeat([]byte{0xAA}, len(tail))
	if !bytes.Equal(tail, want) {
		t.Errorf("sector tail not all 0xAA")
	}
}
