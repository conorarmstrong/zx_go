package opus

import (
	"os"
	"path/filepath"
	"testing"
)

// Opus Discovery disks are 40 tracks of 18 256-byte sectors on one side, a
// flat 184320-byte image with no header. Confirmed against three unrelated
// real .OPD images, all exactly that size.

func TestImageGeometry(t *testing.T) {
	if Cylinders != 40 || SectorsPerTrack != 18 || SectorSize != 256 {
		t.Fatalf("geometry = %d/%d/%d, want 40/18/256", Cylinders, SectorsPerTrack, SectorSize)
	}
	if ImageBytes != 184320 {
		t.Errorf("ImageBytes = %d, want 184320", ImageBytes)
	}
}

func TestLoadImageRejectsWrongSize(t *testing.T) {
	for _, n := range []int{0, 1, 184319, 184321, 163840 /* a TR-DOS 40SS */} {
		if _, err := LoadImage(make([]byte, n)); err == nil {
			t.Errorf("LoadImage accepted a %d-byte image", n)
		}
	}
	if _, err := LoadImage(make([]byte, ImageBytes)); err != nil {
		t.Errorf("LoadImage rejected a correctly sized image: %v", err)
	}
}

func TestSectorAddressing(t *testing.T) {
	img, err := LoadImage(make([]byte, ImageBytes))
	if err != nil {
		t.Fatal(err)
	}
	// Tracks and sectors are both 0-based, laid out track by track. The
	// Opus numbers sectors from zero: its drive table records "(03) no extra
	// sectors" and the ROM asks for track 0, sector 0 to reach the first
	// block of the disk.
	for _, tc := range []struct {
		track, sector, wantOff int
	}{
		{0, 0, 0},
		{0, 1, 256},
		{0, 17, 17 * 256},
		{1, 0, 18 * 256},
		{39, 17, 184320 - 256},
	} {
		off, err := img.offset(tc.track, tc.sector)
		if err != nil {
			t.Fatalf("offset(%d,%d): %v", tc.track, tc.sector, err)
		}
		if off != tc.wantOff {
			t.Errorf("offset(track %d, sector %d) = %d, want %d",
				tc.track, tc.sector, off, tc.wantOff)
		}
	}
	for _, tc := range []struct{ track, sector int }{
		{-1, 0}, {40, 0}, {0, -1}, {0, 18},
	} {
		if _, err := img.offset(tc.track, tc.sector); err == nil {
			t.Errorf("offset(track %d, sector %d) accepted an out-of-range address", tc.track, tc.sector)
		}
	}
}

func TestSectorRoundTrip(t *testing.T) {
	img, _ := LoadImage(make([]byte, ImageBytes))
	buf := make([]byte, SectorSize)
	for i := range buf {
		buf[i] = byte(i)
	}
	if err := img.WriteSector(7, 5, buf); err != nil {
		t.Fatal(err)
	}
	got, err := img.ReadSector(7, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		if got[i] != buf[i] {
			t.Fatalf("sector byte %d = %#02x, want %#02x", i, got[i], buf[i])
		}
	}
	// And it must not have disturbed a neighbour.
	nb, _ := img.ReadSector(7, 6)
	for i, b := range nb {
		if b != 0 {
			t.Fatalf("neighbouring sector byte %d = %#02x, want 0", i, b)
		}
	}
}

// TestRealDiskImages loads the actual .OPD images if they are present
// (gitignored, under games/opus) and checks they parse and carry data.
func TestRealDiskImages(t *testing.T) {
	dir := filepath.Join("..", "..", "games", "opus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no local Opus disks: %v", err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".OPD" && filepath.Ext(e.Name()) != ".opd" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		img, err := LoadImage(data)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		// Track 0 sector 1 must exist and not be blank — every Opus disk has
		// a boot/catalogue sector there.
		s, err := img.ReadSector(0, 1)
		if err != nil {
			t.Errorf("%s: read track 0 sector 1: %v", e.Name(), err)
			continue
		}
		blank := true
		for _, b := range s {
			if b != 0x00 && b != 0xFF {
				blank = false
				break
			}
		}
		if blank {
			t.Errorf("%s: track 0 sector 1 is blank", e.Name())
		}
		n++
	}
	if n == 0 {
		t.Skip("no .OPD images present")
	}
	t.Logf("parsed %d real Opus disk images", n)
}

// TestImageTracksModification pins the dirty flag that tells the emulator
// whether a mounted disk has anything worth writing back. Without it a "save
// disk" action either always rewrites the file or never offers itself.
func TestImageTracksModification(t *testing.T) {
	img := NewImage()
	if img.Modified() {
		t.Error("a fresh image reports itself modified")
	}
	buf, err := img.ReadSector(3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if img.Modified() {
		t.Error("reading a sector marked the image modified")
	}
	buf[0] = 0x99
	if err := img.WriteSector(3, 4, buf); err != nil {
		t.Fatal(err)
	}
	if !img.Modified() {
		t.Error("writing a sector did not mark the image modified")
	}

	// A failed write must not set it.
	fresh := NewImage()
	if err := fresh.WriteSector(0, SectorsPerTrack, buf); err == nil {
		t.Fatal("precondition: that write should have been rejected")
	}
	if fresh.Modified() {
		t.Error("a rejected write marked the image modified")
	}
}

// TestImageRoundTripsThroughBytes pins that a mounted image can be written
// back out byte-identically, so saving a disk that was only read cannot
// corrupt it.
func TestImageRoundTripsThroughBytes(t *testing.T) {
	src := make([]byte, ImageBytes)
	for i := range src {
		src[i] = byte(i * 7)
	}
	img, err := LoadImage(src)
	if err != nil {
		t.Fatal(err)
	}
	out := img.Bytes()
	if len(out) != ImageBytes {
		t.Fatalf("Bytes() returned %d bytes, want %d", len(out), ImageBytes)
	}
	for i := range src {
		if out[i] != src[i] {
			t.Fatalf("byte %d changed: %#02x -> %#02x", i, src[i], out[i])
		}
	}
	// And it must be a copy, not the live buffer.
	out[0] ^= 0xFF
	if img.Data[0] == out[0] {
		t.Error("Bytes() aliases the image's own storage")
	}
}
