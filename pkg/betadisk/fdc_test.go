package betadisk

import "testing"

// Tracer bullet: a Restore command seeks the head to track 0, leaves the track
// register at 0, asserts the Type-I Track-0 status bit, raises INTRQ, and
// finishes not-busy.
func TestFDCRestore(t *testing.T) {
	f := NewFDC()
	f.Mount(0, NewImage(80, 2))
	f.SelectDrive(0)
	f.WriteTrackReg(20) // pretend the head register thinks it's at track 20

	f.WriteCommand(0x00) // Restore (Type I)

	if f.ReadTrackReg() != 0 {
		t.Errorf("track register = %d after Restore, want 0", f.ReadTrackReg())
	}
	// INTRQ must be live before the status register is read (reading status
	// clears it, per the WD1793).
	if !f.Intrq() {
		t.Error("INTRQ not raised after Restore")
	}
	st := f.ReadStatus()
	if st&statusBusy != 0 {
		t.Errorf("still busy after Restore: status=%02X", st)
	}
	if st&statusTrack0 == 0 {
		t.Errorf("Track-0 status bit not set: status=%02X", st)
	}
	// Reading the status register cleared INTRQ.
	if f.Intrq() {
		t.Error("INTRQ should clear after reading the status register")
	}
}

// Read Sector streams all 256 bytes through the data register, asserting DRQ
// for each, and completes (Busy clear, INTRQ set) after the last byte.
func TestFDCReadSector(t *testing.T) {
	f := NewFDC()
	img := NewImage(80, 2)
	want := make([]byte, SectorSize)
	for i := range want {
		want[i] = byte(i)
	}
	if err := img.WriteSector(5, 1, 3, want); err != nil {
		t.Fatal(err)
	}
	f.Mount(0, img)
	f.SelectDrive(0)
	f.SetSide(1)
	// Seek the head to cylinder 5, point the sector register at sector 3.
	f.WriteData(5)
	f.WriteCommand(0x10) // Seek
	f.WriteSectorReg(3)

	f.WriteCommand(0x80) // Read Sector (Type II)
	if !f.Drq() {
		t.Fatal("DRQ not asserted at start of Read Sector")
	}
	got := make([]byte, SectorSize)
	for i := 0; i < SectorSize; i++ {
		if f.ReadStatus()&statusBusy == 0 {
			t.Fatalf("not busy at byte %d", i)
		}
		got[i] = f.ReadData()
	}
	if f.ReadStatus()&statusBusy != 0 {
		t.Error("still busy after final byte")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %02X, want %02X", i, got[i], want[i])
		}
	}
}

// Write Sector accepts 256 bytes and commits them to the mounted image.
func TestFDCWriteSectorRoundTrip(t *testing.T) {
	f := NewFDC()
	img := NewImage(80, 2)
	f.Mount(0, img)
	f.SelectDrive(0)
	f.SetSide(0)
	f.WriteData(7)
	f.WriteCommand(0x10) // Seek to cylinder 7
	f.WriteSectorReg(9)

	f.WriteCommand(0xA0) // Write Sector
	for i := 0; i < SectorSize; i++ {
		if !f.Drq() {
			t.Fatalf("DRQ low at byte %d of write", i)
		}
		f.WriteData(byte(255 - i))
	}
	if f.ReadStatus()&statusBusy != 0 {
		t.Error("still busy after write completes")
	}
	got, err := img.ReadSector(7, 0, 9)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != byte(255-i) {
			t.Fatalf("committed byte %d = %02X, want %02X", i, got[i], byte(255-i))
		}
	}
}

// A write to a write-protected disk fails immediately with the WP status bit
// and never enters a transfer.
func TestFDCWriteProtected(t *testing.T) {
	f := NewFDC()
	f.Mount(0, NewImage(80, 2))
	f.SetWriteProtect(0, true)
	f.SelectDrive(0)
	f.WriteSectorReg(1)
	f.WriteCommand(0xA0) // Write Sector
	if f.ReadStatus()&statusWrProt == 0 {
		t.Error("write-protect status bit not set")
	}
	if f.Drq() {
		t.Error("DRQ should not assert on a write-protected disk")
	}
}

// Read Address streams the 6-byte ID field and copies the track into the sector
// register, as the WD1793 does.
func TestFDCReadAddress(t *testing.T) {
	f := NewFDC()
	f.Mount(0, NewImage(80, 2))
	f.SelectDrive(0)
	f.WriteData(11)
	f.WriteCommand(0x10) // Seek to cylinder 11 → track register = 11
	f.WriteCommand(0xC0) // Read Address
	first := f.ReadData()
	if first != 11 {
		t.Errorf("first ID byte (track) = %d, want 11", first)
	}
	if f.ReadSectorReg() != 11 {
		t.Errorf("sector register after Read Address = %d, want 11 (track copy)", f.ReadSectorReg())
	}
}

// Reading a sector that doesn't exist reports Record Not Found.
func TestFDCRecordNotFound(t *testing.T) {
	f := NewFDC()
	f.Mount(0, NewImage(80, 2))
	f.SelectDrive(0)
	f.WriteSectorReg(99) // no such sector
	f.WriteCommand(0x80) // Read Sector
	if f.ReadStatus()&statusRNF == 0 {
		t.Error("RNF status bit not set for a missing sector")
	}
}

// With no disk mounted, commands report Not Ready.
func TestFDCNotReady(t *testing.T) {
	f := NewFDC()
	f.SelectDrive(1) // empty drive
	f.WriteCommand(0x80)
	if f.ReadStatus()&statusNotRdy == 0 {
		t.Error("Not Ready status bit not set with no disk")
	}
}

// Force Interrupt aborts an in-flight transfer and clears Busy.
func TestFDCForceInterrupt(t *testing.T) {
	f := NewFDC()
	f.Mount(0, NewImage(80, 2))
	f.SelectDrive(0)
	f.WriteSectorReg(1)
	f.WriteCommand(0x80) // Read Sector — starts a transfer
	if f.ReadStatus()&statusBusy == 0 {
		t.Fatal("expected Busy mid-transfer")
	}
	f.WriteCommand(0xD0) // Force Interrupt
	if f.ReadStatus()&statusBusy != 0 {
		t.Error("Busy not cleared by Force Interrupt")
	}
}
