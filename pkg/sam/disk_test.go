package sam

import "testing"

func TestLoadMGT800K(t *testing.T) {
	data := make([]byte, mgt800KSize)
	// Marker at (cyl=1, head=1, sector=3): MGT is cylinder-major.
	off := ((1*mgtHeads+1)*mgt800KSectors + (3 - 1)) * mgtSectorSize
	data[off] = 0x5A
	d, err := LoadDisk(data)
	if err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}
	if c, h, s, sz := d.Geometry(); c != 80 || h != 2 || s != 10 || sz != 512 {
		t.Errorf("MGT geometry = %dx%dx%d size %d, want 80x2x10x512", c, h, s, sz)
	}
	sec, ok := d.ReadSector(1, 1, 3)
	if !ok || sec[0] != 0x5A {
		t.Errorf("MGT ReadSector(1,1,3)[0] = %#02x ok=%v, want 0x5A", sec[0], ok)
	}
}

func TestLoadMGT720K(t *testing.T) {
	d, err := LoadDisk(make([]byte, mgt720KSize))
	if err != nil {
		t.Fatalf("LoadDisk 720K: %v", err)
	}
	if _, _, s, _ := d.Geometry(); s != 9 {
		t.Errorf("720K MGT should be 9 sectors/track, got %d", s)
	}
}

func TestLoadSAD(t *testing.T) {
	heads, cyls, sectors, size := 2, 80, 10, 512
	data := make([]byte, sadHeaderLen+cyls*heads*sectors*size)
	copy(data, sadSignature)
	data[18] = byte(heads)
	data[19] = byte(cyls)
	data[20] = byte(sectors)
	data[21] = byte(size / sadSizeDivisor)
	// SAD is head-major: track = head*cyls + cyl.
	off := sadHeaderLen + ((1*cyls+1)*sectors+(3-1))*size
	data[off] = 0xC3
	d, err := LoadDisk(data)
	if err != nil {
		t.Fatalf("LoadDisk SAD: %v", err)
	}
	if c, h, s, sz := d.Geometry(); c != 80 || h != 2 || s != 10 || sz != 512 {
		t.Errorf("SAD geometry = %dx%dx%d size %d", c, h, s, sz)
	}
	sec, ok := d.ReadSector(1, 1, 3)
	if !ok || sec[0] != 0xC3 {
		t.Errorf("SAD ReadSector(1,1,3)[0] = %#02x ok=%v, want 0xC3 (head-major)", sec[0], ok)
	}
}

func TestLoadDiskUnrecognised(t *testing.T) {
	if _, err := LoadDisk(make([]byte, 1234)); err == nil {
		t.Error("expected error for an unrecognised disk image size")
	}
}

func TestDiskWriteProtect(t *testing.T) {
	d := blankMGT()
	d.SetWriteProtect(true)
	if d.WriteSector(0, 0, 1, make([]byte, 512)) {
		t.Error("write to a write-protected disk should fail")
	}
	d.SetWriteProtect(false)
	if !d.WriteSector(0, 0, 1, make([]byte, 512)) {
		t.Error("write to a writable disk should succeed")
	}
}
