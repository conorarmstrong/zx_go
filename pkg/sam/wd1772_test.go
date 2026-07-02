package sam

import "testing"

// diskWithSector builds an 800K MGT disk with a recognisable sector at
// (cyl, head, sector): byte i = base+i.
func diskWithSector(cyl, head, sector int, base byte) *Disk {
	d := blankMGT()
	buf := make([]byte, 512)
	for i := range buf {
		buf[i] = base + byte(i)
	}
	d.WriteSector(cyl, head, sector, buf)
	return d
}

func TestWD1772SeekAndRestore(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(blankMGT())
	f.data = 5
	f.WriteCommand(0x10) // SEEK to data register (5)
	if f.ReadTrack() != 5 || f.cyl != 5 {
		t.Errorf("SEEK: track=%d cyl=%d, want 5", f.ReadTrack(), f.cyl)
	}
	if f.ReadStatus()&wdBusy != 0 {
		t.Error("instantaneous seek should not leave BUSY set")
	}
	f.WriteCommand(0x00) // RESTORE
	if f.cyl != 0 || f.ReadStatus()&wdTrack00 == 0 {
		t.Errorf("RESTORE should seek to track 0 and report TRACK00 (cyl=%d status=%#02x)", f.cyl, f.ReadStatus())
	}
}

func TestWD1772ReadSector(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(diskWithSector(2, 1, 4, 0x40))
	f.cyl = 2
	f.SetSide(1)
	f.WriteSector(4)     // sector register
	f.WriteCommand(0x80) // READ SECTOR
	if f.ReadStatus()&wdDRQ == 0 {
		t.Fatal("READ SECTOR should set DRQ")
	}
	for i := 0; i < 512; i++ {
		if got := f.ReadData(); got != 0x40+byte(i) {
			t.Fatalf("ReadData[%d] = %#02x, want %#02x", i, got, 0x40+byte(i))
		}
	}
	if f.ReadStatus()&(wdBusy|wdDRQ) != 0 {
		t.Error("after the last byte BUSY and DRQ should clear")
	}
}

func TestWD1772WriteSector(t *testing.T) {
	d := blankMGT()
	f := NewWD1772()
	f.InsertDisk(d)
	f.cyl = 0
	f.SetSide(0)
	f.WriteSector(1)
	f.WriteCommand(0xA0) // WRITE SECTOR
	if f.ReadStatus()&wdDRQ == 0 {
		t.Fatal("WRITE SECTOR should set DRQ")
	}
	for i := 0; i < 512; i++ {
		f.WriteData(byte(0x90) + byte(i))
	}
	sec, _ := d.ReadSector(0, 0, 1)
	for i := 0; i < 512; i++ {
		if sec[i] != byte(0x90)+byte(i) {
			t.Fatalf("written sector mismatch at %d: %#02x, want %#02x", i, sec[i], byte(0x90)+byte(i))
		}
	}
}

// TestWD1772WriteSectorMultiple drives a WRITE SECTOR command with the
// multiple-record flag (m=1) across a whole 10-sector track: DRQ must stay
// asserted past the first sector boundary (the controller keeps transferring
// into sector 2 instead of ending the command), and the command only
// completes once it runs off the end of the track (sector 11 doesn't exist).
func TestWD1772WriteSectorMultiple(t *testing.T) {
	d := blankMGT() // 800K MGT: 10 sectors/track
	f := NewWD1772()
	f.InsertDisk(d)
	f.cyl = 0
	f.SetSide(0)
	f.WriteSector(1)
	f.WriteCommand(0xB0) // WRITE SECTOR, m=1 (multiple record)
	const sectors = 10
	for sec := 1; sec <= sectors; sec++ {
		if f.ReadStatus()&wdDRQ == 0 {
			t.Fatalf("sector %d: DRQ should be set mid-transfer", sec)
		}
		for i := 0; i < 512; i++ {
			f.WriteData(byte(sec*0x10) + byte(i))
		}
	}
	if f.ReadStatus()&(wdBusy|wdDRQ) != 0 {
		t.Error("after running off the end of the track BUSY and DRQ should clear")
	}
	for sec := 1; sec <= sectors; sec++ {
		got, ok := d.ReadSector(0, 0, sec)
		if !ok {
			t.Fatalf("sector %d not written", sec)
		}
		for i := 0; i < 512; i++ {
			if want := byte(sec*0x10) + byte(i); got[i] != want {
				t.Fatalf("sector %d byte %d = %#02x, want %#02x", sec, i, got[i], want)
			}
		}
	}
}

func TestWD1772RecordNotFound(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(blankMGT())
	f.WriteSector(99) // sector 99 doesn't exist (10/track)
	f.WriteCommand(0x80)
	if f.ReadStatus()&wdRNF == 0 {
		t.Errorf("reading a non-existent sector should set RNF, status=%#02x", f.ReadStatus())
	}
}

func TestWD1772LostDataTimeout(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(diskWithSector(0, 0, 1, 0))
	f.WriteSector(1)
	f.WriteCommand(0x80) // READ SECTOR — DRQ set, but we never read the data
	for i := 0; i < wdLostDataReads+1; i++ {
		f.ReadStatus()
	}
	if f.ReadStatus()&wdLostData == 0 {
		t.Errorf("unserviced DRQ should time out to LOST_DATA, status=%#02x", f.ReadStatus())
	}
}

func TestWD1772NoDiskNotReady(t *testing.T) {
	f := NewWD1772()
	if f.ReadStatus()&wdNotReady == 0 {
		t.Error("a controller with no disk should report not-ready (bit 7)")
	}
}

// TestSAMFloppyReadsSector drives the FDC through the SAM port interface:
// drive 1 status/track/sector/data on 0xE0-E7, side via port bit 2.
func TestSAMFloppyReadsSector(t *testing.T) {
	m, _ := NewFromROM(synthROM())
	m.InsertDisk(0, diskWithSector(0, 0, 1, 0x11))
	m.WritePort(0x00E2, 1)    // sector register (port 0xE2 = reg 2)
	m.WritePort(0x00E0, 0x80) // command register (0xE0 = reg 0): READ SECTOR
	if st, _ := m.ReadPort(0x00E0); st&wdDRQ == 0 {
		t.Fatalf("FDC should signal DRQ via the SAM port, status=%#02x", st)
	}
	first, _ := m.ReadPort(0x00E3) // data register (0xE3 = reg 3)
	if first != 0x11 {
		t.Errorf("first sector byte via SAM port = %#02x, want 0x11", first)
	}
}
