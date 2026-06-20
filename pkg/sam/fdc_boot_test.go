package sam

import "testing"

// The SAM boot ROM issues a RESTORE then reads the status to decide whether a
// disk is present and spinning. On a WD1772 a Type I status read reports SPIN_UP
// (motor spin-up complete) when a disk is in the drive; without it the ROM
// reports "Missing disk" and never reads a sector.
func TestTypeIReportsSpinUpWithDisk(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(blankMGT())
	f.WriteCommand(0x00) // RESTORE
	s := f.ReadStatus()
	if s&wdSpinUp == 0 {
		t.Errorf("RESTORE with a disk must report SPIN_UP (0x20); status=%#02x", s)
	}
	if s&wdTrack00 == 0 {
		t.Errorf("RESTORE must report TRACK00; status=%#02x", s)
	}
	if s&wdBusy != 0 {
		t.Errorf("instantaneous seek should not leave BUSY set; status=%#02x", s)
	}
}

// After a FORCE INTERRUPT the WD1772 reverts to Type I status reporting, so a
// status read shows the drive's spin-up/track state. SAMDOS reads the status
// after loading the DOS and reports "Check disk in drive" (error 87) if it does
// not see the disk ready here.
func TestForceInterruptRevertsToTypeIStatus(t *testing.T) {
	f := NewWD1772()
	f.InsertDisk(blankMGT())
	f.WriteCommand(0x80) // READ SECTOR — a Type II command (cmdType1 = false)
	f.WriteCommand(0xD0) // FORCE INTERRUPT
	s := f.ReadStatus()
	if s&wdSpinUp == 0 {
		t.Errorf("after FORCE INTERRUPT with a disk, status must report Type I SPIN_UP; got %#02x", s)
	}
	if s&wdBusy != 0 {
		t.Errorf("FORCE INTERRUPT should clear BUSY; got %#02x", s)
	}
}

func TestTypeINoDiskNoSpinUp(t *testing.T) {
	f := NewWD1772() // no disk
	f.WriteCommand(0x00)
	s := f.ReadStatus()
	if s&wdSpinUp != 0 {
		t.Errorf("with no disk there is no spin-up; status=%#02x", s)
	}
	if s&wdNotReady == 0 {
		t.Errorf("with no disk the drive is not ready; status=%#02x", s)
	}
}
