package plus3fdc

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildSyntheticUDI constructs a minimal UDI file in memory with two
// cylinders and one side. Each track has a single sector's worth of
// data laid out via trackBuilder so the result is parseable by idAtCRC
// and dataAt.
func buildSyntheticUDI(t *testing.T) []byte {
	t.Helper()

	const (
		cylinders = 2
		sides     = 1
		bpt       = 6250
	)

	var tracks bytes.Buffer
	for c := 0; c < cylinders; c++ {
		for h := 0; h < sides; h++ {
			tr := newTrack(bpt, 0xE5, byte(c), byte(h), 2)
			b := newTrackBuilder(tr, gapMFM)
			b.preindexAdd()
			b.postindexAdd()
			b.idAdd(byte(c), byte(h), 1, 2, false)
			data := make([]byte, 512)
			for i := range data {
				data[i] = byte(c<<4 | h<<3 | i&7)
			}
			b.dataAdd(data, false, false)
			b.gap4Add()

			// Encode as type 0x01 (MFM basic).
			tracks.WriteByte(udiTypeMFM)
			var bptBuf [2]byte
			binary.LittleEndian.PutUint16(bptBuf[:], uint16(bpt))
			tracks.Write(bptBuf[:])
			tracks.Write(tr.data)
			tracks.Write(tr.clocks)
		}
	}

	// Build the main header.
	hdr := make([]byte, 16)
	copy(hdr[0:4], udiSignature)
	// UDI stores (file length - 4) — the CRC32 footer at the end is
	// not included. Our file = 16 header + tracks + 4 CRC, so the
	// stored field is 16 + len(tracks).
	eof := uint32(16 + tracks.Len())
	binary.LittleEndian.PutUint32(hdr[4:8], eof)
	hdr[8] = 0             // version
	hdr[9] = cylinders - 1 // cyl - 1
	hdr[10] = sides - 1    // sides - 1

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(tracks.Bytes())
	// Append a placeholder 4-byte CRC32. We don't verify it.
	out.Write([]byte{0, 0, 0, 0})
	return out.Bytes()
}

func TestParseUDI(t *testing.T) {
	data := buildSyntheticUDI(t)
	d, err := ParseUDI(data)
	if err != nil {
		t.Fatalf("ParseUDI: %v", err)
	}
	if d.Cylinders != 2 {
		t.Errorf("cylinders: got %d, want 2", d.Cylinders)
	}
	if d.Sides != 1 {
		t.Errorf("sides: got %d, want 1", d.Sides)
	}

	// Walk sector 1 of each track and verify the recognisable pattern.
	for c := 0; c < 2; c++ {
		tr := d.Track(0, c)
		if tr == nil {
			t.Errorf("track h=0 c=%d missing", c)
			continue
		}
		s := tr.FindSector(1)
		if s == nil {
			t.Errorf("track h=0 c=%d: sector R=1 not found", c)
			continue
		}
		for i := 0; i < 8; i++ {
			want := byte(c<<4 | 0<<3 | i&7)
			if s.Data[i] != want {
				t.Errorf("track h=0 c=%d sector 1 byte %d: got %02X, want %02X",
					c, i, s.Data[i], want)
				break
			}
		}
	}
}

func TestLoadDiskAutoDetect(t *testing.T) {
	// DSK signature → ParseDSK path.
	dsk, _ := buildSyntheticDSK(t, false)
	if _, err := ParseDiskImage(dsk); err != nil {
		t.Errorf("DSK via ParseDiskImage: %v", err)
	}
	// EDSK signature → ParseDSK path.
	edsk, _ := buildSyntheticDSK(t, true)
	if _, err := ParseDiskImage(edsk); err != nil {
		t.Errorf("EDSK via ParseDiskImage: %v", err)
	}
	// UDI signature → ParseUDI path.
	udi := buildSyntheticUDI(t)
	if _, err := ParseDiskImage(udi); err != nil {
		t.Errorf("UDI via ParseDiskImage: %v", err)
	}
	// Unknown signature → error.
	bogus := make([]byte, 256)
	copy(bogus, []byte("not a real disk image xyz"))
	if _, err := ParseDiskImage(bogus); err == nil {
		t.Error("ParseDiskImage: expected error on unrecognised signature, got nil")
	}
}
