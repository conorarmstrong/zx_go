package betadisk

import "testing"

// A freshly created double-sided 80-track image is the canonical TR-DOS size:
// 2 sides × 80 cylinders × 16 sectors × 256 bytes = 655360 bytes.
func TestNewImageDoubleSidedSize(t *testing.T) {
	img := NewImage(80, 2)
	if got := len(img.Data); got != 80*2*16*256 {
		t.Fatalf("DS image size = %d, want %d", got, 80*2*16*256)
	}
	if img.Cylinders != 80 || img.Sides != 2 {
		t.Fatalf("geometry = %dc/%ds, want 80c/2s", img.Cylinders, img.Sides)
	}
}

// A sector written at (cylinder, side, sector) reads back identically, and the
// (C,S,R)→offset mapping is the de-facto TRD layout: side-interleaved within a
// cylinder, sector numbers 1-based.
func TestImageSectorRoundTrip(t *testing.T) {
	img := NewImage(80, 2)
	want := make([]byte, 256)
	for i := range want {
		want[i] = byte(i ^ 0x5A)
	}
	// cylinder 3, side 1, sector 7 (1-based).
	if err := img.WriteSector(3, 1, 7, want); err != nil {
		t.Fatalf("WriteSector: %v", err)
	}
	got, err := img.ReadSector(3, 1, 7)
	if err != nil {
		t.Fatalf("ReadSector: %v", err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %02X, want %02X", i, got[i], want[i])
		}
	}
	// Verify the offset is exactly where the TRD layout says it is.
	off := ((3*2 + 1) * 16 * 256) + (7-1)*256
	if img.Data[off] != want[0] {
		t.Fatalf("data at offset %d = %02X, want %02X", off, img.Data[off], want[0])
	}
}

// Out-of-geometry accesses are rejected, not silently wrapped.
func TestImageBoundsChecked(t *testing.T) {
	img := NewImage(80, 2)
	cases := []struct{ c, s, r int }{
		{80, 0, 1}, // cylinder past end
		{0, 2, 1},  // side past end
		{0, 0, 0},  // sector 0 (TR-DOS sectors are 1..16)
		{0, 0, 17}, // sector past end
	}
	for _, tc := range cases {
		if _, err := img.ReadSector(tc.c, tc.s, tc.r); err == nil {
			t.Errorf("ReadSector(%d,%d,%d) = nil err, want out-of-range", tc.c, tc.s, tc.r)
		}
	}
}

// LoadImage infers single- vs double-sided 80/40-track geometry from the byte
// count of a raw .TRD dump, disambiguating the 327680-byte collision (40-track
// DS vs 80-track SS) via the disk-type byte at offset 0x8E3.
func TestLoadImageGeometryInference(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bytes     int
		diskType  byte // disk-type byte at 0x8E3; 0 = leave blank
		cyl, side int
	}{
		{"80 track DS", 80 * 2 * 16 * 256, 0x16, 80, 2},
		{"40 track DS", 40 * 2 * 16 * 256, 0x17, 40, 2},
		{"80 track SS", 80 * 1 * 16 * 256, 0x18, 80, 1},
		{"40 track SS", 40 * 1 * 16 * 256, 0x19, 40, 1},
		// A blank ambiguous-size image defaults to the common 40-track DS.
		{"ambiguous blank", 327680, 0x00, 40, 2},
	} {
		data := make([]byte, tc.bytes)
		if tc.diskType != 0 {
			data[0x8E3] = tc.diskType
		}
		img, err := LoadImage(data)
		if err != nil {
			t.Errorf("%s: LoadImage: %v", tc.name, err)
			continue
		}
		if img.Cylinders != tc.cyl || img.Sides != tc.side {
			t.Errorf("%s: geometry %dc/%ds, want %dc/%ds", tc.name, img.Cylinders, img.Sides, tc.cyl, tc.side)
		}
	}
	if _, err := LoadImage(make([]byte, 1234)); err == nil {
		t.Error("LoadImage of a non-TRD-sized buffer should error")
	}
}
