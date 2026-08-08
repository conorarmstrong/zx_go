package opus

import (
	"os"
	"path/filepath"
	"testing"
)

// The Opus Discovery's WD1770 shares the Western Digital FD179x command set.
// These drive it exactly as the guest does — through the memory-mapped
// register file — so a passing test means the guest's own access path works.

// mountReal mounts the first real .OPD image found, or skips.
func mountReal(t *testing.T) (*Device, *Image) {
	t.Helper()
	dir := filepath.Join("..", "..", "games", "opus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no local Opus disks: %v", err)
	}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); ext != ".OPD" && ext != ".opd" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		img, err := LoadImage(data)
		if err != nil {
			t.Fatal(err)
		}
		d := New()
		d.Mount(0, img)
		return d, img
	}
	t.Skip("no .OPD images present")
	return nil, nil
}

// TestReadSectorFromRealDisk is the end-to-end check of the guest's path: a
// Type II READ SECTOR issued through the register file must hand back the
// bytes the image holds, one at a time via the data register.
func TestReadSectorFromRealDisk(t *testing.T) {
	d, img := mountReal(t)
	want, err := img.ReadSector(0, 1)
	if err != nil {
		t.Fatal(err)
	}

	d.Write(CtrlBase, 0x00)  // select drive 0
	d.Write(FDCBase, 0x00)   // RESTORE -> track 0
	d.Write(FDCBase+1, 0x00) // track register
	d.Write(FDCBase+2, 0x01) // sector 1
	d.Write(FDCBase, 0x80)   // READ SECTOR

	got := make([]byte, SectorSize)
	for i := range got {
		got[i] = d.Read(FDCBase + 3) // data register
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d of track 0 sector 1 = %#02x, want %#02x", i, got[i], want[i])
		}
	}
	if st := d.Read(FDCBase); st&StatusDRQ != 0 {
		t.Errorf("DRQ still asserted after the whole sector was read (status %#02x)", st)
	}
}

// TestReadSectorHonoursTrackAndSector pins that the address registers are
// actually used, not ignored in favour of a fixed sector.
func TestReadSectorHonoursTrackAndSector(t *testing.T) {
	img := NewImage()
	// Stamp each sector with its own track/sector so a mis-address shows.
	for tr := 0; tr < Cylinders; tr++ {
		for se := 1; se <= SectorsPerTrack; se++ {
			buf := make([]byte, SectorSize)
			buf[0], buf[1] = byte(tr), byte(se)
			_ = img.WriteSector(tr, se, buf)
		}
	}
	d := New()
	d.Mount(0, img)

	for _, tc := range []struct{ track, sector int }{{0, 1}, {5, 9}, {39, 18}} {
		d.Write(FDCBase+1, byte(tc.track))
		d.Write(FDCBase+2, byte(tc.sector))
		d.Write(FDCBase, 0x80) // READ SECTOR
		gotTrack := d.Read(FDCBase + 3)
		gotSector := d.Read(FDCBase + 3)
		if int(gotTrack) != tc.track || int(gotSector) != tc.sector {
			t.Errorf("read track %d sector %d returned stamp %d/%d",
				tc.track, tc.sector, gotTrack, gotSector)
		}
		// Drain the rest so the next command starts clean.
		for i := 2; i < SectorSize; i++ {
			d.Read(FDCBase + 3)
		}
	}
}

// TestWriteSectorReachesTheImage pins the write path.
func TestWriteSectorReachesTheImage(t *testing.T) {
	img := NewImage()
	d := New()
	d.Mount(0, img)

	d.Write(FDCBase+1, 3) // track 3
	d.Write(FDCBase+2, 4) // sector 4
	d.Write(FDCBase, 0xA0)
	for i := 0; i < SectorSize; i++ {
		d.Write(FDCBase+3, byte(i^0x5A))
	}
	got, err := img.ReadSector(3, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if want := byte(i ^ 0x5A); got[i] != want {
			t.Fatalf("written byte %d = %#02x, want %#02x", i, got[i], want)
		}
	}
}

// TestReadWithNoDiskSetsRecordNotFound pins the empty-drive behaviour: the
// controller must report an error rather than hand back stale bytes.
func TestReadWithNoDiskSetsRecordNotFound(t *testing.T) {
	d := New()
	d.Write(FDCBase+2, 1)
	d.Write(FDCBase, 0x80)
	if st := d.Read(FDCBase); st&StatusRecordNotFound == 0 {
		t.Errorf("status %#02x with no disk mounted, want RECORD NOT FOUND set", st)
	}
}

// TestSeekUpdatesTheTrackRegister pins Type I seeking.
func TestSeekUpdatesTheTrackRegister(t *testing.T) {
	d := New()
	d.Mount(0, NewImage())
	d.Write(FDCBase+3, 20) // data register = target track
	d.Write(FDCBase, 0x10) // SEEK
	if got := d.Read(FDCBase + 1); got != 20 {
		t.Errorf("track register after SEEK = %d, want 20", got)
	}
}
