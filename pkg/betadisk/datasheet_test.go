package betadisk

// Datasheet-faithful test suite for the WD1793 floppy-disk controller.
//
// The oracle throughout is the Western Digital WD1793 / FD179X-02 datasheet
// ("FD179X-02 Floppy Disk Formatter/Controller Family"). Every assertion below
// cites the datasheet behaviour it pins. No emulator is used as a reference;
// these are derived purely from the published chip specification.
//
// Command encoding (WD1793 datasheet, "Command Summary"):
//
//	Type I   Restore   0000 hVr1r0   (h=head load, V=verify, r=step rate)
//	         Seek      0001 hVr1r0
//	         Step      001u hVr1r0   (u=update track register)
//	         Step In   010u hVr1r0
//	         Step Out  011u hVr1r0
//	Type II  Read Sec  100m Sec0     (m=multiple)
//	         Write Sec 101m Sec0a0
//	Type III Read Addr 1100 0E00
//	         Read Trk  1110 0E00
//	         Write Trk 1111 0E00
//	Type IV  Force Int 1101 IiIiIiIi
//
// Status-register bit meaning depends on the command class. From the datasheet
// status tables:
//
//	bit  Type I              Type II / III
//	---  ------------------  ---------------------------
//	7    Not Ready           Not Ready
//	6    Write Protect       Write Protect (write cmds)
//	5    Head Loaded         Record Type / Write Fault
//	4    Seek Error          Record Not Found
//	3    CRC Error           CRC Error
//	2    Track 0             Lost Data
//	1    Index               Data Request (DRQ)
//	0    Busy                Busy

import "testing"

// trdImage builds a small in-memory TR-DOS .TRD image with each sector filled
// with a deterministic, position-dependent pattern so reads can be checked
// against a known value addressed by (cylinder, side, sector).
func trdImage(t *testing.T, cylinders, sides int) *Image {
	t.Helper()
	img := NewImage(cylinders, sides)
	buf := make([]byte, SectorSize)
	for c := 0; c < cylinders; c++ {
		for s := 0; s < sides; s++ {
			for sec := 1; sec <= SectorsPerTrack; sec++ {
				seed := byte(c*7 + s*131 + sec*17)
				for i := range buf {
					buf[i] = seed + byte(i)
				}
				if err := img.WriteSector(c, s, sec, buf); err != nil {
					t.Fatalf("seed WriteSector(%d,%d,%d): %v", c, s, sec, err)
				}
			}
		}
	}
	return img
}

// ----------------------------------------------------------------------------
// Type I commands: Restore, Seek, Step, Step-In, Step-Out.
// ----------------------------------------------------------------------------

// WD1793 Restore (Type I): steps the head out until the TRACK00 input is true,
// loads 0 into the Track Register, and asserts the Track-0 status bit. After
// completion Busy is clear and INTRQ is raised.
func TestTypeIRestoreToTrack0(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	// Park the head away from track 0 first via a Seek.
	f.WriteData(40)
	f.WriteCommand(0x10) // Seek to track 40
	if f.headTrack[0] != 40 {
		t.Fatalf("setup: head at %d, want 40", f.headTrack[0])
	}

	f.WriteCommand(0x00) // Restore

	if f.headTrack[0] != 0 {
		t.Errorf("head at track %d after Restore, want 0", f.headTrack[0])
	}
	if f.ReadTrackReg() != 0 {
		t.Errorf("Track Register = %d after Restore, want 0", f.ReadTrackReg())
	}
	if !f.Intrq() {
		t.Error("INTRQ not raised at Restore completion")
	}
	st := f.statusReg
	if st&statusBusy != 0 {
		t.Errorf("Busy still set after Restore: %02X", st)
	}
	if st&statusTrack0 == 0 {
		t.Errorf("Track-0 status bit (bit 2) not set: %02X", st)
	}
	if st&statusHeadDmp == 0 {
		t.Errorf("Head-Loaded status bit (bit 5) not set: %02X", st)
	}
}

// WD1793 Seek (Type I): the controller seeks to the track held in the Data
// Register, copying it into the Track Register on completion. The Track-0 bit
// must reflect the destination (clear when seeking away from 0, set at 0).
func TestTypeISeekUpdatesTrackRegisterAndTrack0(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)

	f.WriteData(33)
	f.WriteCommand(0x10) // Seek to track 33
	if f.ReadTrackReg() != 33 {
		t.Errorf("Track Register = %d after Seek 33, want 33", f.ReadTrackReg())
	}
	if f.statusReg&statusTrack0 != 0 {
		t.Errorf("Track-0 bit set after seeking to track 33: %02X", f.statusReg)
	}

	f.WriteData(0)
	f.WriteCommand(0x10) // Seek back to track 0
	if f.statusReg&statusTrack0 == 0 {
		t.Errorf("Track-0 bit clear after seeking to track 0: %02X", f.statusReg)
	}
}

// WD1793 Step-In (Type I): moves the head one track toward the spindle (higher
// track numbers). With the update-track-register (u) flag, bit 4 of the
// command, the Track Register is incremented to match.
func TestTypeIStepInUpdateFlag(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteCommand(0x00) // Restore to track 0

	// Step In WITHOUT the update flag: head advances, Track Register stays.
	f.WriteCommand(0x40) // Step In, u=0
	if f.headTrack[0] != 1 {
		t.Errorf("head at %d after Step-In, want 1", f.headTrack[0])
	}
	if f.ReadTrackReg() != 0 {
		t.Errorf("Track Register changed to %d with u=0, want unchanged 0", f.ReadTrackReg())
	}

	// Step In WITH the update flag (bit 4): Track Register tracks the head.
	f.WriteCommand(0x50) // Step In, u=1
	if f.headTrack[0] != 2 {
		t.Errorf("head at %d after second Step-In, want 2", f.headTrack[0])
	}
	if f.ReadTrackReg() != 2 {
		t.Errorf("Track Register = %d after Step-In u=1, want 2", f.ReadTrackReg())
	}
}

// WD1793 Step-Out (Type I): moves the head one track away from the spindle
// (toward track 0). The head cannot step past track 0.
func TestTypeIStepOutTowardTrack0(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteData(3)
	f.WriteCommand(0x10) // Seek to track 3

	f.WriteCommand(0x70) // Step Out, u=1
	if f.headTrack[0] != 2 || f.ReadTrackReg() != 2 {
		t.Errorf("after Step-Out: head=%d trackReg=%d, want 2/2", f.headTrack[0], f.ReadTrackReg())
	}

	// Step Out repeatedly cannot drive the head below track 0.
	for i := 0; i < 5; i++ {
		f.WriteCommand(0x60) // Step Out, u=0
	}
	if f.headTrack[0] != 0 {
		t.Errorf("head at %d after over-stepping out, want clamped to 0", f.headTrack[0])
	}
	if f.statusReg&statusTrack0 == 0 {
		t.Errorf("Track-0 bit not set at track 0: %02X", f.statusReg)
	}
}

// WD1793 Step (Type I): a bare Step command repeats the direction of the last
// Step-In or Step-Out issued (the datasheet's "last direction" latch).
func TestTypeIStepRepeatsLastDirection(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteData(10)
	f.WriteCommand(0x10) // Seek to 10

	f.WriteCommand(0x40) // Step In → latches direction = in
	in := f.headTrack[0]
	f.WriteCommand(0x20) // bare Step → should also step in
	if f.headTrack[0] != in+1 {
		t.Errorf("bare Step after Step-In: head=%d, want %d (repeat inward)", f.headTrack[0], in+1)
	}

	f.WriteCommand(0x60) // Step Out → latches direction = out
	out := f.headTrack[0]
	f.WriteCommand(0x20) // bare Step → should also step out
	if f.headTrack[0] != out-1 {
		t.Errorf("bare Step after Step-Out: head=%d, want %d (repeat outward)", f.headTrack[0], out-1)
	}
}

// WD1793 Type I completion: the status register uses the Type-I bit meanings,
// so the Data-Request line (Drq()) — which is a Type II/III concept — must be
// reported as inactive even though bit 1 (the Index bit in Type I) may be busy.
func TestTypeIStatusIsNotDRQ(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteCommand(0x00) // Restore (Type I)
	if f.Drq() {
		t.Error("Drq() reported true for a Type I command; DRQ is a Type II/III status bit")
	}
}

// ----------------------------------------------------------------------------
// Type I status with no disk: the Not-Ready bit (bit 7) dominates.
// ----------------------------------------------------------------------------

// WD1793: with the drive not ready, a Type I command sets the Not-Ready status
// bit (bit 7) and does not assert Head-Loaded.
func TestTypeINotReady(t *testing.T) {
	f := NewFDC()
	f.SelectDrive(0) // no disk mounted
	f.WriteCommand(0x00)
	if f.statusReg&statusNotRdy == 0 {
		t.Errorf("Not-Ready bit (bit 7) not set with no disk: %02X", f.statusReg)
	}
}

// ----------------------------------------------------------------------------
// Type II commands: Read Sector, Write Sector — DRQ handshake & RNF path.
// ----------------------------------------------------------------------------

// WD1793 Read Sector (Type II): on locating the sector the controller asserts
// Busy and DRQ; a DRQ is generated for each of the 256 bytes; reading the Data
// Register resets DRQ until the next byte. After the final byte Busy clears and
// INTRQ is raised. The status register here uses Type II bit meanings, so bit 1
// is DRQ (not Index).
func TestTypeIIReadSectorDRQHandshake(t *testing.T) {
	f := NewFDC()
	img := trdImage(t, 80, 2)
	f.Mount(0, img)
	f.SelectDrive(0)
	f.SetSide(1)
	f.WriteData(12)
	f.WriteCommand(0x10) // Seek to cylinder 12
	f.WriteSectorReg(5)

	want, err := img.ReadSector(12, 1, 5)
	if err != nil {
		t.Fatal(err)
	}

	f.WriteCommand(0x80) // Read Sector
	// At the start of the transfer Busy and DRQ (bit 1) are both set.
	if f.statusReg&statusBusy == 0 {
		t.Fatalf("Busy not set at Read Sector start: %02X", f.statusReg)
	}
	if f.statusReg&statusDRQ == 0 {
		t.Fatalf("DRQ (bit 1) not set at Read Sector start: %02X", f.statusReg)
	}
	if !f.Drq() {
		t.Fatal("Drq() false at Read Sector start")
	}

	got := make([]byte, SectorSize)
	for i := 0; i < SectorSize; i++ {
		if f.statusReg&statusDRQ == 0 {
			t.Fatalf("DRQ not asserted before reading byte %d", i)
		}
		got[i] = f.ReadData()
	}
	// After the last byte the transfer is complete.
	if f.statusReg&statusBusy != 0 {
		t.Errorf("Busy still set after last byte: %02X", f.statusReg)
	}
	if f.statusReg&statusDRQ != 0 {
		t.Errorf("DRQ still set after last byte: %02X", f.statusReg)
	}
	if !f.Intrq() {
		t.Error("INTRQ not raised at Read Sector completion")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %02X, want %02X", i, got[i], want[i])
		}
	}
}

// WD1793 Record-Not-Found (Type II): if the requested sector's ID field cannot
// be located, the Record-Not-Found status bit (bit 4) is set and no transfer
// begins (DRQ stays low, Busy is cleared).
func TestTypeIIReadSectorRecordNotFound(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteSectorReg(99) // out of the 1..16 range — no such ID field
	f.WriteCommand(0x80) // Read Sector
	if f.statusReg&statusRNF == 0 {
		t.Errorf("Record-Not-Found bit (bit 4) not set: %02X", f.statusReg)
	}
	if f.Drq() {
		t.Error("DRQ asserted on a record-not-found read")
	}
	if f.statusReg&statusBusy != 0 {
		t.Errorf("Busy left set on RNF: %02X", f.statusReg)
	}
}

// WD1793 Write Sector (Type II): the controller asserts DRQ for each byte the
// CPU must supply; once 256 bytes are written the data is committed and Busy
// clears. A round trip through the same image proves the data path.
func TestTypeIIWriteSectorDRQHandshake(t *testing.T) {
	f := NewFDC()
	img := trdImage(t, 80, 2)
	f.Mount(0, img)
	f.SelectDrive(0)
	f.SetSide(0)
	f.WriteData(20)
	f.WriteCommand(0x10) // Seek to cylinder 20
	f.WriteSectorReg(8)

	f.WriteCommand(0xA0) // Write Sector
	if f.statusReg&statusBusy == 0 || f.statusReg&statusDRQ == 0 {
		t.Fatalf("Busy|DRQ not set at Write Sector start: %02X", f.statusReg)
	}
	src := make([]byte, SectorSize)
	for i := range src {
		src[i] = byte(0xC3 ^ i)
		if f.statusReg&statusDRQ == 0 {
			t.Fatalf("DRQ low before writing byte %d", i)
		}
		f.WriteData(src[i])
	}
	if f.statusReg&statusBusy != 0 {
		t.Errorf("Busy still set after Write Sector completes: %02X", f.statusReg)
	}
	if !f.Intrq() {
		t.Error("INTRQ not raised at Write Sector completion")
	}
	got, err := img.ReadSector(20, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != src[i] {
			t.Fatalf("committed byte %d = %02X, want %02X", i, got[i], src[i])
		}
	}
}

// WD1793 Write Sector to a write-protected disk: the Write-Protect status bit
// (bit 6) is set and no transfer begins.
func TestTypeIIWriteProtect(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SetWriteProtect(0, true)
	f.SelectDrive(0)
	f.WriteSectorReg(1)
	f.WriteCommand(0xA0) // Write Sector
	if f.statusReg&statusWrProt == 0 {
		t.Errorf("Write-Protect bit (bit 6) not set: %02X", f.statusReg)
	}
	if f.Drq() {
		t.Error("DRQ asserted on a write-protected write")
	}
}

// WD1793 Write Sector with the drive not ready sets the Not-Ready bit (bit 7).
func TestTypeIIWriteSectorNotReady(t *testing.T) {
	f := NewFDC()
	f.SelectDrive(2) // empty
	f.WriteSectorReg(1)
	f.WriteCommand(0xA0)
	if f.statusReg&statusNotRdy == 0 {
		t.Errorf("Not-Ready bit (bit 7) not set on write with no disk: %02X", f.statusReg)
	}
}

// WD1793 Read Sector reads the sector selected by the Sector Register: changing
// only the Sector Register between two reads on the same track yields the data
// of two different sectors.
func TestTypeIIReadSectorSelectsBySectorRegister(t *testing.T) {
	f := NewFDC()
	img := trdImage(t, 80, 2)
	f.Mount(0, img)
	f.SelectDrive(0)
	f.SetSide(0)
	f.WriteData(4)
	f.WriteCommand(0x10) // Seek to cylinder 4

	read := func(sec int) byte {
		f.WriteSectorReg(byte(sec))
		f.WriteCommand(0x80)
		return f.ReadData() // first byte of the sector
	}
	b1 := read(1)
	b2 := read(2)
	w1, _ := img.ReadSector(4, 0, 1)
	w2, _ := img.ReadSector(4, 0, 2)
	if b1 != w1[0] {
		t.Errorf("sector 1 first byte = %02X, want %02X", b1, w1[0])
	}
	if b2 != w2[0] {
		t.Errorf("sector 2 first byte = %02X, want %02X", b2, w2[0])
	}
	if b1 == b2 {
		t.Error("sector 1 and sector 2 returned the same first byte; Sector Register not honoured")
	}
}

// ----------------------------------------------------------------------------
// Type III command: Read Address.
// ----------------------------------------------------------------------------

// WD1793 Read Address (Type III): the next ID field is streamed as six bytes —
// Track address, Side number, Sector address, Sector Length code, CRC1, CRC2 —
// each accompanied by a DRQ. The datasheet also specifies that the Track
// address byte is loaded into the Sector Register.
func TestTypeIIIReadAddressIDField(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.SetSide(1)
	f.WriteData(27)
	f.WriteCommand(0x10) // Seek to track 27 (sets Track Register = 27)

	f.WriteCommand(0xC0) // Read Address
	if f.statusReg&statusBusy == 0 || f.statusReg&statusDRQ == 0 {
		t.Fatalf("Busy|DRQ not set at Read Address start: %02X", f.statusReg)
	}
	id := make([]byte, 6)
	for i := range id {
		if f.statusReg&statusDRQ == 0 {
			t.Fatalf("DRQ low before ID byte %d", i)
		}
		id[i] = f.ReadData()
	}
	// Byte 0: Track address.
	if id[0] != 27 {
		t.Errorf("ID[0] (track) = %d, want 27", id[0])
	}
	// Byte 1: Side number.
	if id[1] != 1 {
		t.Errorf("ID[1] (side) = %d, want 1", id[1])
	}
	// Byte 3: Sector Length code; 0x01 == 256 bytes (TR-DOS sector size).
	if id[3] != 0x01 {
		t.Errorf("ID[3] (length code) = %02X, want 01 (256 bytes)", id[3])
	}
	// Datasheet: Read Address copies the Track address into the Sector Register.
	if f.ReadSectorReg() != 27 {
		t.Errorf("Sector Register = %d after Read Address, want 27 (track copy)", f.ReadSectorReg())
	}
	if f.statusReg&statusBusy != 0 {
		t.Errorf("Busy still set after the 6-byte ID field: %02X", f.statusReg)
	}
}

// WD1793 Read Address with no disk sets the Not-Ready bit.
func TestTypeIIIReadAddressNotReady(t *testing.T) {
	f := NewFDC()
	f.SelectDrive(0) // empty
	f.WriteCommand(0xC0)
	if f.statusReg&statusNotRdy == 0 {
		t.Errorf("Not-Ready bit not set on Read Address with no disk: %02X", f.statusReg)
	}
}

// ----------------------------------------------------------------------------
// Type IV command: Force Interrupt.
// ----------------------------------------------------------------------------

// WD1793 Force Interrupt (Type IV) issued during a command: the command is
// terminated and the Busy status bit is reset. An in-flight read transfer is
// aborted and DRQ drops.
func TestTypeIVForceInterruptAbortsTransfer(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteSectorReg(1)
	f.WriteCommand(0x80) // Read Sector — Busy + DRQ
	f.ReadData()         // consume one byte; transfer mid-flight
	if f.statusReg&statusBusy == 0 {
		t.Fatal("setup: expected Busy mid-transfer")
	}

	f.WriteCommand(0xD0) // Force Interrupt
	if f.statusReg&statusBusy != 0 {
		t.Errorf("Busy not reset by Force Interrupt: %02X", f.statusReg)
	}
	if f.Drq() {
		t.Error("DRQ still asserted after Force Interrupt aborted the transfer")
	}
}

// WD1793 Force Interrupt issued while IDLE: "the Busy Status bit is reset and
// the rest of the status bits are updated or cleared. In this case, Status
// reflects the Type I commands." So with a ready drive whose head is at track 0
// the status register must show the Type-I Head-Loaded and Track-0 bits, and
// the controller's status-bit interpretation must switch to Type I (DRQ inert).
func TestTypeIVForceInterruptIdleReflectsTypeI(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteCommand(0x00) // Restore — head at track 0, idle
	f.ReadStatus()       // clear INTRQ from the Restore

	f.WriteCommand(0xD0) // Force Interrupt while idle

	st := f.statusReg
	if st&statusBusy != 0 {
		t.Errorf("Busy set after idle Force Interrupt: %02X", st)
	}
	// Type I status while idle and ready: Head Loaded (bit 5) and Track 0 (bit 2).
	if st&statusHeadDmp == 0 {
		t.Errorf("Head-Loaded bit (bit 5) not set after idle Force Interrupt: %02X", st)
	}
	if st&statusTrack0 == 0 {
		t.Errorf("Track-0 bit (bit 2) not set after idle Force Interrupt at track 0: %02X", st)
	}
	// The status register now carries Type I meanings, so the Type II/III DRQ
	// reading must be inert.
	if f.Drq() {
		t.Error("Drq() true after idle Force Interrupt; status now reflects Type I")
	}
}

// WD1793 Force Interrupt issued while idle with the drive not ready reflects
// Type I status with the Not-Ready bit set.
func TestTypeIVForceInterruptIdleNotReady(t *testing.T) {
	f := NewFDC()
	f.SelectDrive(0) // no disk
	f.WriteCommand(0xD0)
	if f.statusReg&statusNotRdy == 0 {
		t.Errorf("Not-Ready bit not set on idle Force Interrupt with no disk: %02X", f.statusReg)
	}
}

// ----------------------------------------------------------------------------
// Registers and the INTRQ line.
// ----------------------------------------------------------------------------

// WD1793: reading the Status Register resets the INTRQ line.
func TestStatusReadClearsIntrq(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteCommand(0x00) // Restore raises INTRQ
	if !f.Intrq() {
		t.Fatal("INTRQ not raised after Restore")
	}
	f.ReadStatus()
	if f.Intrq() {
		t.Error("INTRQ not cleared by reading the Status Register")
	}
}

// WD1793: the Track and Sector registers are directly readable and writable and
// hold their value independently of any command.
func TestTrackSectorRegistersReadWrite(t *testing.T) {
	f := NewFDC()
	f.WriteTrackReg(0x2A)
	f.WriteSectorReg(0x0C)
	if f.ReadTrackReg() != 0x2A {
		t.Errorf("Track Register = %02X, want 2A", f.ReadTrackReg())
	}
	if f.ReadSectorReg() != 0x0C {
		t.Errorf("Sector Register = %02X, want 0C", f.ReadSectorReg())
	}
}

// WD1793: writing the Data Register before a Type I Seek supplies the seek
// destination; the value latched there drives the seek.
func TestDataRegisterDrivesSeek(t *testing.T) {
	f := NewFDC()
	f.Mount(0, trdImage(t, 80, 2))
	f.SelectDrive(0)
	f.WriteData(55)
	f.WriteCommand(0x10) // Seek
	if f.ReadTrackReg() != 55 {
		t.Errorf("Seek used Data Register %d, Track Register = %d, want 55", 55, f.ReadTrackReg())
	}
}

// ----------------------------------------------------------------------------
// Status-bit overlap: bit 1 and bit 2 carry different meanings per command type.
// This pins that the constants alias correctly (datasheet status tables).
// ----------------------------------------------------------------------------

func TestStatusBitAliasesMatchDatasheet(t *testing.T) {
	// Bit 1 (0x02): Index in Type I, Data Request in Type II/III.
	if statusIndex != 0x02 || statusDRQ != 0x02 {
		t.Errorf("bit-1 aliases wrong: Index=%02X DRQ=%02X, want 02/02", statusIndex, statusDRQ)
	}
	// Bit 2 (0x04): Track 0 in Type I, Lost Data in Type II/III.
	if statusTrack0 != 0x04 || statusLost != 0x04 {
		t.Errorf("bit-2 aliases wrong: Track0=%02X Lost=%02X, want 04/04", statusTrack0, statusLost)
	}
	// Bit 4 (0x10): Seek Error in Type I, Record Not Found in Type II/III.
	if statusSeekErr != 0x10 || statusRNF != 0x10 {
		t.Errorf("bit-4 aliases wrong: SeekErr=%02X RNF=%02X, want 10/10", statusSeekErr, statusRNF)
	}
	// Bit 5 (0x20): Head Loaded in Type I, Write Fault in Type II write.
	if statusHeadDmp != 0x20 || statusWrFault != 0x20 {
		t.Errorf("bit-5 aliases wrong: HeadLoaded=%02X WriteFault=%02X, want 20/20", statusHeadDmp, statusWrFault)
	}
	// Fixed-meaning bits across all types.
	if statusBusy != 0x01 || statusCRCErr != 0x08 || statusWrProt != 0x40 || statusNotRdy != 0x80 {
		t.Errorf("fixed bits wrong: Busy=%02X CRC=%02X WrProt=%02X NotRdy=%02X",
			statusBusy, statusCRCErr, statusWrProt, statusNotRdy)
	}
}

// ----------------------------------------------------------------------------
// System register ($FF) and end-to-end through the Beta port decode.
// ----------------------------------------------------------------------------

// Beta system register ($FF): drive select (bits 0-1) and side select
// (bit 4, inverted) route the WD1793 to the correct (drive, side). A read of a
// sector on side 1 vs side 0 of the same physical drive must differ.
func TestSystemRegisterSideSelectRoutesRead(t *testing.T) {
	it := NewInterface()
	img := trdImage(t, 80, 2)
	it.FDC.Mount(0, img)

	readSide := func(sysSide byte) byte {
		// bits: drive 0, bit2=1 (no reset), bit4 = sysSide.
		it.WritePort(0xFF, 0x04|sysSide)
		it.WritePort(0x3F, 0) // Track Register = 0
		it.WritePort(0x5F, 1) // Sector Register = 1
		it.WritePort(0x1F, 0x80)
		return it.ReadPort(0x7F)
	}
	side0 := readSide(0x10) // bit4 set → side 0
	side1 := readSide(0x00) // bit4 clear → side 1
	w0, _ := img.ReadSector(0, 0, 1)
	w1, _ := img.ReadSector(0, 1, 1)
	if side0 != w0[0] {
		t.Errorf("side-0 read first byte = %02X, want %02X", side0, w0[0])
	}
	if side1 != w1[0] {
		t.Errorf("side-1 read first byte = %02X, want %02X", side1, w1[0])
	}
}

// Beta system register reset (bit 2 clear) performs a WD1793 master reset:
// any in-flight transfer is aborted and Busy clears.
func TestSystemRegisterMasterReset(t *testing.T) {
	it := NewInterface()
	it.FDC.Mount(0, trdImage(t, 80, 2))
	it.WritePort(0xFF, 0x04) // no reset
	it.FDC.WriteSectorReg(1)
	it.FDC.WriteCommand(0x80) // Read Sector — Busy
	if it.ReadPort(0x1F)&statusBusy == 0 {
		t.Fatal("setup: expected Busy before reset")
	}
	it.WritePort(0xFF, 0x00) // bit2 clear → master reset
	if it.ReadPort(0x1F)&statusBusy != 0 {
		t.Error("Busy not cleared by master reset")
	}
}

// Beta $FF read reports INTRQ in bit 7 and DRQ in bit 6, the two WD1793 output
// lines the Beta hardware wires into the system-register read.
func TestSystemRegisterReadIntrqDrq(t *testing.T) {
	it := NewInterface()
	it.FDC.Mount(0, trdImage(t, 80, 2))
	it.WritePort(0xFF, 0x04)
	it.FDC.WriteCommand(0x00) // Restore → INTRQ
	if it.ReadPort(0xFF)&0x80 == 0 {
		t.Error("INTRQ (bit 7) not reported by $FF read")
	}
	it.FDC.WriteSectorReg(1)
	it.FDC.WriteCommand(0x80) // Read Sector → DRQ
	if it.ReadPort(0xFF)&0x40 == 0 {
		t.Error("DRQ (bit 6) not reported by $FF read")
	}
}

// Beta full I/O path: a complete sector streamed out through the data port
// matches the image, exactly as the TR-DOS ROM's read loop drives it.
func TestEndToEndReadThroughPorts(t *testing.T) {
	it := NewInterface()
	img := trdImage(t, 80, 2)
	it.FDC.Mount(0, img)
	it.WritePort(0xFF, 0x10|0x04) // drive 0, side 0, no reset
	// Seek to cylinder 6 via Data Register + Seek.
	it.WritePort(0x7F, 6)
	it.WritePort(0x1F, 0x10) // Seek
	it.WritePort(0x5F, 11)   // Sector Register = 11
	it.WritePort(0x1F, 0x80) // Read Sector

	want, _ := img.ReadSector(6, 0, 11)
	for i := 0; i < SectorSize; i++ {
		if got := it.ReadPort(0x7F); got != want[i] {
			t.Fatalf("byte %d via ports = %02X, want %02X", i, got, want[i])
		}
	}
}
