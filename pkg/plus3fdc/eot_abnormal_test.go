package plus3fdc

import "testing"

// The +3 never asserts the µPD765's Terminal Count line. Every transfer
// therefore ends because the controller itself stopped, and the datasheet
// defines IC=01 as "execution of the command was started but was not
// successfully completed" — which is exactly that case. ST1.EN additionally
// marks the transfer that stopped because it would have had to step past the
// last sector of the cylinder.
//
// Comando Quatro's loader is the evidence for the read path. Its read routine
// checks the three status bytes literally:
//
//	FDCC  LD A,($FECA) / CP $40 / JR NZ,fail   ; ST0 must be $40
//	FDD3  LD A,($FECB) / CP $80 / JR NZ,fail   ; ST1 must be $80
//	FDDA  LD A,($FECC) / AND A  / JR NZ,fail   ; ST2 must be $00
//
// Reporting a normal termination failed that check, so the loader retried
// three times, gave up one sector into the 40766 bytes it wanted, and jumped
// into memory it had never filled.
func TestEndOfCylinderIsAnAbnormalTermination(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46) // READ DATA
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT = R: the transfer ends at the last sector
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if res[1] != st1EN {
		t.Errorf("ST1 = %02X, want %02X (EN alone)", res[1], st1EN)
	}
	if res[2] != 0x00 {
		t.Errorf("ST2 = %02X, want 00", res[2])
	}
	if got := res[0] & (st0IC0 | st0IC1); got != st0IC0 {
		t.Errorf("ST0 = %02X, interrupt code %02X — want an abnormal termination", res[0], got)
	}
}

// EN says the FDC tried to step past the final sector. A transfer that
// stopped earlier for its own reason never made that attempt, so EN must
// stay clear and the host can tell "bad sector" from "ran off the cylinder".
func TestAbortBeforeEOTDoesNotSetEndOfCylinder(t *testing.T) {
	// Sector 2 of three carries a deleted DAM, so a READ DATA with SK=0
	// stops there rather than running on to EOT=3.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	for r := byte(1); r <= 3; r++ {
		b.idAdd(0, 0, r, 2, false)
		b.dataAdd(make([]byte, 512), r == 2, false)
	}
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)

	f.WriteData(0x46) // READ DATA, SK=0
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02)
	f.WriteData(0x03) // EOT = 3
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 2*512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if res[2]&st2CM == 0 {
		t.Fatalf("ST2 = %02X, CM clear — the test needs the deleted DAM to stop the transfer", res[2])
	}
	if res[1]&st1EN != 0 {
		t.Errorf("ST1 = %02X, EN set after stopping at sector 2 of EOT=3 — the FDC never tried past EOT", res[1])
	}
	// It still did not transfer what was asked for, so it is not a success.
	if got := res[0] & (st0IC0 | st0IC1); got != st0IC0 {
		t.Errorf("ST0 = %02X, interrupt code %02X — 2 of 3 sectors is not a successful completion", res[0], got)
	}
}

// A data CRC error at the final sector terminates the command there. The FDC
// never attempts the sector after EOT, so EN must stay clear and the host
// sees a clean "bad data" report rather than a mixed $A0.
func TestCRCErrorAtEOTDoesNotSetEndOfCylinder(t *testing.T) {
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), false, true) // deliberately bad data CRC
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)

	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01) // EOT = R
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if res[1]&st1DE == 0 {
		t.Fatalf("ST1 = %02X, DE clear — the test needs the bad data CRC to be detected", res[1])
	}
	if res[1]&st1EN != 0 {
		t.Errorf("ST1 = %02X, EN set alongside DE — the command stopped at the bad sector, "+
			"it never tried to step past EOT", res[1])
	}
}

// WRITE DATA ends the same way a read does: the +3 asserts no Terminal Count,
// so a write that reaches EOT stopped of its own accord and says so. Reads
// and writes with identical geometry must not disagree.
func TestWriteEndOfCylinderMatchesRead(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x45) // WRITE DATA
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT = R
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.WriteData(0xA5)
	}
	res := drainResult(t, f, 7)

	if res[1]&st1EN == 0 {
		t.Errorf("write ST1 = %02X, EN clear at end of cylinder", res[1])
	}
	if got := res[0] & (st0IC0 | st0IC1); got != st0IC0 {
		t.Errorf("write ST0 = %02X, interrupt code %02X — want the same abnormal "+
			"termination a read of the same geometry reports", res[0], got)
	}
}

// WRITE DATA spans consecutive sectors up to EOT, as the package header says
// and as READ DATA already does. A single-sector write silently dropped every
// byte after the first sector.
func TestWriteDataSpansSectorsToEOT(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x45)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02)
	f.WriteData(0x03) // EOT = 3, so sectors 1, 2 and 3
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for s := 0; s < 3; s++ {
		for i := 0; i < 512; i++ {
			f.WriteData(byte(0x11 * (s + 1)))
		}
	}
	drainResult(t, f, 7)

	for s := byte(1); s <= 3; s++ {
		sec := f.disks[0].Tracks[0][0].FindSector(s)
		if sec == nil {
			t.Fatalf("sector %d vanished", s)
		}
		want := 0x11 * s
		if sec.Data[0] != want || sec.Data[511] != want {
			t.Errorf("sector %d holds %02X..%02X, want %02X — multi-sector write did not reach it",
				s, sec.Data[0], sec.Data[511], want)
		}
	}
}

// READ DIAGNOSTIC ends on its own byte budget, which is the same kind of
// self-termination, and must report it the same way.
func TestReadDiagnosticEndsAbnormally(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x42) // READ DIAGNOSTIC (READ TRACK), MFM
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01) // EOT = 1, so 512 bytes
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if got := res[0] & (st0IC0 | st0IC1); got != st0IC0 {
		t.Errorf("READ TRACK ST0 = %02X, interrupt code %02X — want an abnormal termination",
			res[0], got)
	}
	if res[1]&st1EN == 0 {
		t.Errorf("READ TRACK ST1 = %02X, EN clear after exhausting the requested length", res[1])
	}
}

// R above EOT never matches, so the FDC keeps stepping sectors until it runs
// out of them and reports "no data" — it does not stop after one sector and
// call that the end of the cylinder.
func TestSectorAboveEOTKeepsStepping(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02)
	f.WriteData(0x00) // EOT = 0, which R never reaches
	f.WriteData(0x2A)
	f.WriteData(0xFF)

	// Drain until the controller leaves execution phase; it must walk off
	// the end of the track rather than stopping after sector 1.
	for i := 0; i < 64*512 && f.phase == phaseExecution; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if res[1]&st1ND == 0 {
		t.Errorf("ST1 = %02X, ND clear — want no-data after stepping past the last sector", res[1])
	}
	if res[1]&st1EN != 0 {
		t.Errorf("ST1 = %02X, EN set — R never equalled EOT, so the cylinder end was never reached", res[1])
	}
}
