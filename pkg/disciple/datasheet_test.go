package disciple

// Datasheet-faithful test suite for the WD1772 floppy-disk controller as used
// by the Miles Gordon Technology +D and DISCiPLE interfaces.
//
// The oracle throughout is the Western Digital WD1772 datasheet ("WD1772
// Floppy Disk Formatter/Controller", the JLG-edited V1.1 reproduction of the
// original WD177X-00 specification). No emulator is used as a reference; every
// assertion is derived from the published chip specification. Citations below
// name the WD1772 datasheet section.
//
// The WD1772 shares the WD177x command model with the WD1793 (the Beta Disk's
// controller) but differs in two datasheet-significant ways exercised here:
//
//   1. STEPPING RATES.  WD1772 Type I rate field r1r0 selects 6/12/2/3 ms
//      (datasheet "Flag Summary"), NOT the WD1793's 3/6/10/15 ms.  These are
//      command-encoding facts; the I/O-advanced controller model performs the
//      head movement instantly, so the rate bits must be accepted and ignored
//      (the head still moves) for every legal r1r0 combination.
//
//   2. STATUS REGISTER.  The WD1772 has NO drive-ready input — pin 19 is the
//      raw Read Data line and there is no Ready signal.  The datasheet "Status
//      Register Summary" therefore gives:
//
//        bit  Type I                 Type II / III
//        ---  ---------------------  ---------------------------
//        7    Motor On               Motor On         (NOT "Not Ready")
//        6    (idle: Write Protect)  Write Protect (write cmds)
//        5    Spin-Up complete       Record Type / (write) Write Fault
//        4    Seek Error             Record Not Found
//        3    CRC Error              CRC Error
//        2    Track 0                Lost Data
//        1    Index Pulse            Data Request (DRQ)
//        0    Busy                   Busy
//
//      So bit 7 is Motor On in EVERY command class (the WD1793's bit-7
//      "Not Ready" does not exist on the WD1772), and the Type I bit-5 meaning
//      is "Spin-Up complete" rather than the WD1793's "Head Loaded".
//
// Command encoding (WD1772 datasheet "Command Summary"):
//
//	Type I   Restore   0000 hVr1r0   (h=motor-on/spin-up, V=verify, r=rate)
//	         Seek      0001 hVr1r0
//	         Step      001u hVr1r0   (u=update track register)
//	         Step In   010u hVr1r0
//	         Step Out  011u hVr1r0
//	Type II  Read Sec  100m hE00     (m=multiple)
//	         Write Sec 101m hEpa0
//	Type III Read Addr 1100 hE00
//	         Read Trk  1110 hE00
//	         Write Trk 1111 hEp0
//	Type IV  Force Int 1101 i3i2i1i0

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
)

// dsNewFDC builds a DISCiPLE with a real (synthetic) GDOS ROM and an attached
// in-memory disk so the WD1772 has media to walk.  The disk is a small MGT
// image whose every sector is filled with a deterministic, position-dependent
// pattern: byte[0]=cylinder, byte[1]=head, byte[2]=sector, the rest a seed so
// reads can be checked against a value addressed by (cylinder, head, sector).
func dsNewFDC(t *testing.T) *Disciple {
	t.Helper()
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	d.disks[0] = dsImage(t)
	return d
}

const (
	dsSides   = 2
	dsCyls    = 80
	dsSectors = 10
	dsSecSize = 512
)

// dsImage builds an in-memory MGT disk via the plus3fdc raw-sector loader so it
// is a genuine spinning-disk byte stream the WD1772 emulation walks (IDAMs +
// DAMs), exactly as a loaded .mgt file would be.
func dsImage(t *testing.T) *plus3fdc.Disk {
	t.Helper()
	img := make([]byte, dsSides*dsCyls*dsSectors*dsSecSize)
	for logical := 0; logical < dsSides*dsCyls; logical++ {
		cyl := logical / dsSides
		head := logical % dsSides
		for s := 0; s < dsSectors; s++ {
			off := (logical*dsSectors + s) * dsSecSize
			img[off] = byte(cyl)
			img[off+1] = byte(head)
			img[off+2] = byte(s + 1)
			seed := byte(cyl*7 + head*131 + (s+1)*17)
			for i := 3; i < dsSecSize; i++ {
				img[off+i] = seed + byte(i)
			}
		}
	}
	disk, err := plus3fdc.ParseMGT(img)
	if err != nil {
		t.Fatalf("ParseMGT: %v", err)
	}
	return disk
}

// dsExpected reproduces dsImage's pattern so reads can be checked.
func dsExpectedFirst3(cyl, head, sec int) (byte, byte, byte) {
	return byte(cyl), byte(head), byte(sec)
}

// Port helpers expressed in WD1772 register terms.
func dsCmd(d *Disciple, v byte)       { d.HandlePortWrite(portFDCCmdStatus, v) }
func dsStatus(d *Disciple) byte       { st, _ := d.HandlePortRead(portFDCCmdStatus); return st }
func dsWriteData(d *Disciple, v byte) { d.HandlePortWrite(portFDCData, v) }
func dsReadData(d *Disciple) byte     { v, _ := d.HandlePortRead(portFDCData); return v }
func dsTrack(d *Disciple) byte        { v, _ := d.HandlePortRead(portFDCTrack); return v }
func dsSetTrack(d *Disciple, v byte)  { d.HandlePortWrite(portFDCTrack, v) }
func dsSector(d *Disciple) byte       { v, _ := d.HandlePortRead(portFDCSector); return v }
func dsSetSector(d *Disciple, v byte) { d.HandlePortWrite(portFDCSector, v) }
func dsSelDrive0Side0(d *Disciple)    { d.HandlePortWrite(portControl, 0x01) } // bit0=1 drive0, bit1=0 side0
func dsSelDrive0Side1(d *Disciple)    { d.HandlePortWrite(portControl, 0x03) } // bit0=1 drive0, bit1=1 side1

// ----------------------------------------------------------------------------
// Type I commands: Restore, Seek, Step, Step-In, Step-Out.
// ----------------------------------------------------------------------------

// WD1772 Restore (datasheet "RESTORE (SEEK TRACK 0)"): steps the head out until
// TR00* is active, loads 0 into the Track Register, and generates an interrupt.
// At track 0 the Track-0 status bit (Type I bit 2) is set.
func TestWD1772RestoreToTrack0(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsSetTrack(d, 40) // park away from track 0
	dsCmd(d, 0x00)    // Restore

	// INTRQ is raised at completion; it is cleared by a Status read, so check
	// it before reading the status register.
	if !d.intrq {
		t.Error("INTRQ not raised at Restore completion")
	}
	if dsTrack(d) != 0 {
		t.Errorf("Track Register = %d after Restore, want 0", dsTrack(d))
	}
	st := dsStatus(d)
	if st&stBusy != 0 {
		t.Errorf("Busy still set after Restore: %02X", st)
	}
	if st&stTrack0 == 0 {
		t.Errorf("Track-0 status bit (bit 2) not set at track 0: %02X", st)
	}
}

// WD1772 Seek (datasheet "SEEK"): the controller seeks to the track held in the
// Data Register and copies it into the Track Register on completion. The Track-0
// bit must reflect the destination (clear when seeking away from 0, set at 0).
func TestWD1772SeekUpdatesTrackRegisterAndTrack0(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)

	dsWriteData(d, 33)
	dsCmd(d, 0x10) // Seek to track 33
	if dsTrack(d) != 33 {
		t.Errorf("Track Register = %d after Seek 33, want 33", dsTrack(d))
	}
	if dsStatus(d)&stTrack0 != 0 {
		t.Errorf("Track-0 bit set after seeking to track 33: %02X", dsStatus(d))
	}

	dsWriteData(d, 0)
	dsCmd(d, 0x10) // Seek back to track 0
	if dsStatus(d)&stTrack0 == 0 {
		t.Errorf("Track-0 bit clear after seeking to track 0: %02X", dsStatus(d))
	}
}

// WD1772 Step-In (datasheet "STEP-IN"): issues one stepping pulse toward track
// 76 (higher track numbers).  With the update flag (u, bit 4) the Track Register
// is incremented to match; without it the Track Register is unchanged.
func TestWD1772StepInUpdateFlag(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore to track 0 (head=0, Track Register=0)

	// Step In WITHOUT the update flag: the head physically advances but the
	// Track Register is left unchanged (datasheet "Flag Summary": u=0 No Update).
	dsCmd(d, 0x40) // Step In, u=0 → head now at track 1
	if dsTrack(d) != 0 {
		t.Errorf("Track Register = %d with u=0, want unchanged 0", dsTrack(d))
	}
	if d.headTrack[0] != 1 {
		t.Errorf("physical head at track %d after u=0 Step-In, want 1", d.headTrack[0])
	}

	// Step In WITH the update flag (bit 4): the head advances again (now track 2)
	// and the Track Register is loaded with the new physical position.
	dsCmd(d, 0x50) // Step In, u=1 → head at track 2, Track Register := 2
	if d.headTrack[0] != 2 {
		t.Errorf("physical head at track %d after u=1 Step-In, want 2", d.headTrack[0])
	}
	if dsTrack(d) != 2 {
		t.Errorf("Track Register = %d after Step-In u=1, want 2 (tracks the head)", dsTrack(d))
	}
}

// WD1772 Step-Out (datasheet "STEP-OUT"): issues one stepping pulse toward track
// 0; with the update flag the Track Register is decremented.  The head cannot be
// driven below track 0 (Restore semantics: TR00* clamps the position).
func TestWD1772StepOutTowardTrack0(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsWriteData(d, 3)
	dsCmd(d, 0x10) // Seek to track 3

	dsCmd(d, 0x70) // Step Out, u=1
	if dsTrack(d) != 2 {
		t.Errorf("Track Register = %d after Step-Out u=1, want 2", dsTrack(d))
	}

	// Step Out repeatedly cannot drive the Track Register below 0.
	for i := 0; i < 5; i++ {
		dsCmd(d, 0x70) // Step Out, u=1
	}
	if dsTrack(d) != 0 {
		t.Errorf("Track Register = %d after over-stepping out, want clamped to 0", dsTrack(d))
	}
	if dsStatus(d)&stTrack0 == 0 {
		t.Errorf("Track-0 bit not set at track 0: %02X", dsStatus(d))
	}
}

// WD1772 stepping rate field (datasheet "Flag Summary"): r1r0 selects the head
// stepping rate — 00=6ms, 01=12ms, 10=2ms, 11=3ms (distinct from the WD1793's
// 3/6/10/15ms).  Whatever the rate, the command must still execute (the head
// moves).  The I/O-advanced model has no real timing, so the requirement is
// simply that every legal r1r0 encoding is accepted and the seek completes.
func TestWD1772SteppingRatesAllAccepted(t *testing.T) {
	for r := byte(0); r < 4; r++ {
		d := dsNewFDC(t)
		dsSelDrive0Side0(d)
		dsWriteData(d, 12)
		// Seek with rate bits r1r0 = r in the low two bits of the command.
		dsCmd(d, 0x10|r)
		if dsTrack(d) != 12 {
			t.Errorf("Seek with rate bits %02b: Track Register = %d, want 12", r, dsTrack(d))
		}
		st := dsStatus(d)
		if st&stBusy != 0 {
			t.Errorf("rate %02b: Busy still set after Seek: %02X", r, st)
		}
	}
}

// WD1772 stepping-rate millisecond table is a datasheet fact distinct from the
// WD1793.  This pins the documented mapping so a future timing model uses the
// WD1772 rates, never the WD1793 ones.
func TestWD1772SteppingRateMillisecondsDatasheet(t *testing.T) {
	// WD1772 datasheet "Flag Summary": r1,r0 = Stepping Rate.
	want := map[byte]int{
		0b00: 6,  // 6 ms
		0b01: 12, // 12 ms
		0b10: 2,  // 2 ms
		0b11: 3,  // 3 ms
	}
	for bits, ms := range want {
		if got := wd1772StepRateMs(bits); got != ms {
			t.Errorf("WD1772 rate bits %02b = %d ms, want %d ms", bits, got, ms)
		}
	}
	// Sanity vs the WD1793, whose rates are 3/6/10/15 ms — the WD1772 must NOT
	// match these, which is the whole point of the difference.
	wd1793 := map[byte]int{0b00: 3, 0b01: 6, 0b10: 10, 0b11: 15}
	for bits := byte(0); bits < 4; bits++ {
		if wd1772StepRateMs(bits) == wd1793[bits] {
			t.Errorf("WD1772 rate bits %02b (%d ms) wrongly equals the WD1793 rate", bits, wd1772StepRateMs(bits))
		}
	}
}

// ----------------------------------------------------------------------------
// Status register: WD1772 has NO Ready line — bit 7 is Motor On in every type.
// ----------------------------------------------------------------------------

// WD1772 datasheet "Status Register Summary": bit 7 (S7) is Motor On in EVERY
// command class.  There is no drive-ready input, so an empty drive must NOT set
// a "Not Ready" bit 7 on a Type I command (the WD1793 behaviour) — bit 7 only
// ever reflects the Motor On output.
func TestWD1772NoReadyBitOnTypeI(t *testing.T) {
	d := dsNewFDC(t)
	d.disks[0] = nil // eject — no media
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore (Type I)

	st := dsStatus(d)
	// A Type I command with no disk must still complete; bit 7 reflects Motor
	// On (the command ran → motor is on), it is never a "Not Ready" indicator.
	if st&stBusy != 0 {
		t.Errorf("Busy left set on a Type I command with no disk: %02X", st)
	}
	if st&stMotorOn == 0 {
		t.Errorf("Motor-On bit (bit 7) not set after a command ran: %02X", st)
	}
}

// WD1772 datasheet: with no media, a Type II Read finds no ID Address Mark
// within 5 revolutions and sets Record Not Found (bit 4) — this, not a Ready
// line, is how the host detects an empty drive.
func TestWD1772EmptyDriveRecordNotFound(t *testing.T) {
	d := dsNewFDC(t)
	d.disks[0] = nil
	dsSelDrive0Side0(d)
	dsSetSector(d, 1)
	dsCmd(d, 0x80) // Read Sector
	st := dsStatus(d)
	if st&stRNF == 0 {
		t.Errorf("Record-Not-Found (bit 4) not set on Read with no disk: %02X", st)
	}
	if d.drq {
		t.Error("DRQ asserted on a no-media read")
	}
}

// WD1772 datasheet "Status Register Summary": Type I bit 5 is "Spin-Up
// complete" (SU), not the WD1793's "Head Loaded".  After a completed Type I
// command with the spin-up sequence enabled (h=0), the motor has spun up, so SU
// (bit 5) and Motor On (bit 7) are both set.
func TestWD1772TypeISpinUpBit(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore with h=0 → spin-up enabled
	st := dsStatus(d)
	if st&stSpinUp == 0 {
		t.Errorf("Spin-Up bit (Type I bit 5) not set after Restore: %02X", st)
	}
	if st&stMotorOn == 0 {
		t.Errorf("Motor-On bit (bit 7) not set after Restore: %02X", st)
	}
}

// WD1772 datasheet "Status Register Description": on Type I commands bit 1 is
// the Index Pulse, not a Data Request.  The Type II/III DRQ concept must report
// inactive after a Type I command.
func TestWD1772TypeIBit1NotDRQ(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore (Type I)
	if d.drq {
		t.Error("drq true after a Type I command; bit 1 is the Index Pulse in Type I, not DRQ")
	}
}

// ----------------------------------------------------------------------------
// Type II commands: Read Sector / Write Sector — DRQ handshake, multi-sector,
// record-not-found.
// ----------------------------------------------------------------------------

// WD1772 Read Sector (datasheet "READ SECTOR"): on locating the sector the
// controller asserts Busy and DRQ; a DRQ accompanies each data byte; reading the
// Data Register clears DRQ until the next byte.  After the final byte Busy
// clears and INTRQ is raised.  Status uses Type II meanings (bit 1 = DRQ).
func TestWD1772ReadSectorDRQHandshake(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side1(d)
	dsWriteData(d, 12)
	dsCmd(d, 0x10) // Seek to cylinder 12
	dsSetSector(d, 5)
	dsCmd(d, 0x80) // Read Sector

	st := dsStatus(d)
	if st&stBusy == 0 {
		t.Fatalf("Busy not set at Read Sector start: %02X", st)
	}
	if st&stDRQ == 0 {
		t.Fatalf("DRQ (bit 1) not set at Read Sector start: %02X", st)
	}
	if !d.drq {
		t.Fatal("drq false at Read Sector start")
	}

	got := make([]byte, dsSecSize)
	for i := 0; i < dsSecSize; i++ {
		if dsStatus(d)&stDRQ == 0 {
			t.Fatalf("DRQ not asserted before reading byte %d", i)
		}
		got[i] = dsReadData(d)
	}
	// INTRQ is raised by the final-byte transfer; check before the Status read
	// (which clears it).
	if !d.intrq {
		t.Error("INTRQ not raised at Read Sector completion")
	}
	st = dsStatus(d)
	if st&stBusy != 0 {
		t.Errorf("Busy still set after last byte: %02X", st)
	}
	if st&stDRQ != 0 {
		t.Errorf("DRQ still set after last byte: %02X", st)
	}
	c, h, s := dsExpectedFirst3(12, 1, 5)
	if got[0] != c || got[1] != h || got[2] != s {
		t.Fatalf("sector (12,1,5) header = %d/%d/%d, want %d/%d/%d", got[0], got[1], got[2], c, h, s)
	}
}

// WD1772 Read Sector selects by the Sector Register (datasheet "TYPE II
// COMMANDS": "the Sector Number of the ID field is compared with the Sector
// Register").  Changing only the Sector Register yields a different sector.
func TestWD1772ReadSectorSelectsBySectorRegister(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsWriteData(d, 4)
	dsCmd(d, 0x10) // Seek to cylinder 4

	// byte[2] of each sector is its sector number; read the three header bytes.
	readSecNum := func(sec byte) byte {
		dsSetSector(d, sec)
		dsCmd(d, 0x80)
		dsReadData(d)        // cyl
		dsReadData(d)        // head
		return dsReadData(d) // sector number
	}
	if got := readSecNum(1); got != 1 {
		t.Errorf("sector 1 header sector-byte = %d, want 1", got)
	}
	if got := readSecNum(2); got != 2 {
		t.Errorf("sector 2 header sector-byte = %d, want 2", got)
	}
}

// WD1772 Read Sector with a sector number not present on the track searches for
// 5 revolutions then sets Record Not Found (datasheet "TYPE II COMMANDS"
// example: reading sector 27 when only 26 exist → RNF).  No transfer begins.
func TestWD1772ReadSectorRecordNotFound(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsSetSector(d, 99) // out of the 1..10 range — no such ID field
	dsCmd(d, 0x80)     // Read Sector
	st := dsStatus(d)
	if st&stRNF == 0 {
		t.Errorf("Record-Not-Found bit (bit 4) not set: %02X", st)
	}
	if d.drq {
		t.Error("DRQ asserted on a record-not-found read")
	}
	if st&stBusy != 0 {
		t.Errorf("Busy left set on RNF: %02X", st)
	}
}

// WD1772 Write Sector (datasheet "WRITE SECTOR"): the controller asserts DRQ for
// each byte the CPU must supply; once the sector is full the data is committed,
// Busy clears, INTRQ is raised.  A round trip proves the data path.
func TestWD1772WriteSectorDRQHandshakeRoundTrip(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsWriteData(d, 20)
	dsCmd(d, 0x10) // Seek to cylinder 20
	dsSetSector(d, 8)
	dsCmd(d, 0xA0) // Write Sector

	st := dsStatus(d)
	if st&stBusy == 0 || st&stDRQ == 0 {
		t.Fatalf("Busy|DRQ not set at Write Sector start: %02X", st)
	}
	src := make([]byte, dsSecSize)
	for i := range src {
		src[i] = byte(0xC3 ^ i)
		if dsStatus(d)&stDRQ == 0 {
			t.Fatalf("DRQ low before writing byte %d", i)
		}
		dsWriteData(d, src[i])
	}
	// INTRQ is raised once the final byte commits; check before the Status read.
	if !d.intrq {
		t.Error("INTRQ not raised at Write Sector completion")
	}
	st = dsStatus(d)
	if st&stBusy != 0 {
		t.Errorf("Busy still set after Write Sector completes: %02X", st)
	}

	// Read the same sector back through the controller and compare.
	dsSetSector(d, 8)
	dsCmd(d, 0x80)
	for i := range src {
		if got := dsReadData(d); got != src[i] {
			t.Fatalf("committed byte %d = %02X, want %02X", i, got, src[i])
		}
	}
}

// WD1772 multi-sector Read (datasheet "TYPE II COMMANDS", m=1): "multiple
// records are read with the Sector Register internally updated ... in numerical
// ascending sequence".  After a Read Sector with m=1 the Sector Register must
// have advanced to the next sector.
func TestWD1772MultiSectorAdvancesSectorRegister(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsWriteData(d, 6)
	dsCmd(d, 0x10) // Seek to cylinder 6
	dsSetSector(d, 3)
	dsCmd(d, 0x90) // Read Sector, m=1 (bit 4)
	// Drain the first sector.
	for i := 0; i < dsSecSize; i++ {
		dsReadData(d)
	}
	if got := dsSector(d); got != 4 {
		t.Errorf("Sector Register = %d after multi-sector read of sector 3, want 4 (auto-advance)", got)
	}
}

// ----------------------------------------------------------------------------
// Type III command: Read Address.
// ----------------------------------------------------------------------------

// WD1772 Read Address (datasheet "Read Address"): the next ID field is streamed
// as six bytes — Track address, Side number, Sector address, Sector Length code,
// CRC1, CRC2 — each with a DRQ.  The datasheet also specifies the Track Address
// of the ID field is written into the Sector Register.
func TestWD1772ReadAddressIDField(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side1(d)
	dsWriteData(d, 27)
	dsCmd(d, 0x10) // Seek to track 27 (Track Register = 27)
	dsCmd(d, 0xC0) // Read Address

	st := dsStatus(d)
	if st&stBusy == 0 || st&stDRQ == 0 {
		t.Fatalf("Busy|DRQ not set at Read Address start: %02X", st)
	}
	id := make([]byte, 6)
	for i := range id {
		if dsStatus(d)&stDRQ == 0 {
			t.Fatalf("DRQ low before ID byte %d", i)
		}
		id[i] = dsReadData(d)
	}
	// Byte 0: Track address.
	if id[0] != 27 {
		t.Errorf("ID[0] (track) = %d, want 27", id[0])
	}
	// Byte 1: Side number.
	if id[1] != 1 {
		t.Errorf("ID[1] (side) = %d, want 1", id[1])
	}
	// Byte 3: Sector Length code; 0x02 == 512 bytes (DISCiPLE sector size).
	if id[3] != 0x02 {
		t.Errorf("ID[3] (length code) = %02X, want 02 (512 bytes)", id[3])
	}
	// Datasheet: Read Address writes the Track address into the Sector Register.
	if dsSector(d) != 27 {
		t.Errorf("Sector Register = %d after Read Address, want 27 (track copy)", dsSector(d))
	}
	if dsStatus(d)&stBusy != 0 {
		t.Errorf("Busy still set after the 6-byte ID field: %02X", dsStatus(d))
	}
}

// ----------------------------------------------------------------------------
// Type III commands: Read Track / Write Track (format).
// ----------------------------------------------------------------------------

// WD1772 Read Track (datasheet "Read Track"): all gap, header and data bytes
// are streamed to the host with a DRQ for each byte; no CRC checking.  Busy is
// set and a transfer of the raw track image begins.
func TestWD1772ReadTrackStreamsRawBytes(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0xE0) // Read Track
	if dsStatus(d)&stRNF != 0 {
		t.Error("Read Track with media should not set RNF")
	}
	if !d.drq || len(d.xferBuf) == 0 {
		t.Error("Read Track should begin a transfer of the raw track bytes")
	}
	if dsStatus(d)&stBusy == 0 {
		t.Error("Read Track should set Busy at the start of the transfer")
	}
}

// WD1772 Write Track (datasheet "WRITE TRACK FORMATTING THE DISK"): formatting
// is done by streaming a full track of data+gap bytes; F5..FE are interpreted as
// address marks.  The command begins a write-direction (format) transfer.
func TestWD1772WriteTrackBeginsFormat(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0xF0) // Write Track
	if dsStatus(d)&stRNF != 0 {
		t.Error("Write Track with media should not set RNF")
	}
	if !d.drq || !d.xferWrite || !d.formatting {
		t.Error("Write Track should begin a format (write) transfer")
	}
}

// ----------------------------------------------------------------------------
// Type IV command: Force Interrupt.
// ----------------------------------------------------------------------------

// WD1772 Force Interrupt (datasheet "TYPE IV COMMANDS"): if a command is under
// execution the Busy bit is reset and the command terminated.  An in-flight read
// transfer is aborted and DRQ drops.
func TestWD1772ForceInterruptAbortsTransfer(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsSetSector(d, 1)
	dsCmd(d, 0x80) // Read Sector → Busy + DRQ
	dsReadData(d)  // consume one byte; transfer mid-flight
	if dsStatus(d)&stBusy == 0 {
		t.Fatal("setup: expected Busy mid-transfer")
	}

	dsCmd(d, 0xD0) // Force Interrupt
	if dsStatus(d)&stBusy != 0 {
		t.Errorf("Busy not reset by Force Interrupt: %02X", dsStatus(d))
	}
	if d.drq {
		t.Error("DRQ still asserted after Force Interrupt aborted the transfer")
	}
	if d.xferBuf != nil {
		t.Error("Force Interrupt should drop the transfer buffer")
	}
}

// WD1772 Force Interrupt issued while a Type II/III command is busy (datasheet
// "Status Register": distinct from the idle case above — only the Busy bit is
// reset; the rest of the status bits, and therefore the Type II/III bit
// meanings, are left as they were). At track 0 with the motor on, a wrong
// switch to Type I meanings would spuriously show the Track-0 (bit 2) and
// Spin-Up (bit 5) bits; correctly it must keep reporting Lost-Data/Record-Type
// (both 0, no such condition occurred).
func TestWD1772ForceInterruptMidTransferKeepsCommandClass(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsSetSector(d, 1)
	dsCmd(d, 0x80) // Read Sector → Busy + DRQ, Type II, track register at 0
	dsReadData(d)  // consume one byte; transfer mid-flight

	dsCmd(d, 0xD0) // Force Interrupt while busy

	if d.lastCmdType1 {
		t.Error("Force Interrupt while busy switched to Type I status; the datasheet keeps the interrupted command's status class")
	}
	st := dsStatus(d)
	if st&stTrack0 != 0 {
		t.Errorf("bit 2 read as Track-0 (Type I) after a busy Force Interrupt: %02X; should read as Lost Data (Type II/III, clear)", st)
	}
	if st&stSpinUp != 0 {
		t.Errorf("bit 5 read as Spin-Up (Type I) after a busy Force Interrupt: %02X; should read as Record Type (Type II/III, clear)", st)
	}
}

// WD1772 Force Interrupt while idle (datasheet "Status Register": "If the Force
// Interrupt Command is received when there is not a current command under
// execution, the Busy Status Bit is reset and ... Status reflects the Type I
// commands.").  So after an idle Force Interrupt at track 0 the status shows the
// Type I Track-0 and Spin-Up/Motor-On bits and the controller is in Type I mode.
func TestWD1772ForceInterruptIdleReflectsTypeI(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore — head at track 0, idle
	dsStatus(d)    // read clears INTRQ

	dsCmd(d, 0xD0) // Force Interrupt while idle

	st := dsStatus(d)
	if st&stBusy != 0 {
		t.Errorf("Busy set after idle Force Interrupt: %02X", st)
	}
	if st&stTrack0 == 0 {
		t.Errorf("Track-0 bit (bit 2) not set after idle Force Interrupt at track 0: %02X", st)
	}
	// Status now carries Type I meanings: the Type II/III DRQ read must be inert.
	if d.drq {
		t.Error("drq true after idle Force Interrupt; status now reflects Type I")
	}
	if !d.lastCmdType1 {
		t.Error("controller not in Type I status mode after idle Force Interrupt")
	}
}

// ----------------------------------------------------------------------------
// Status-register bit aliasing (datasheet "Status Register Summary").
// ----------------------------------------------------------------------------

// The WD1772 status bits 1,2,4,5 carry different meanings per command class, and
// bit 7 is Motor On (NOT the WD1793's Not Ready).  This pins the constants.
func TestWD1772StatusBitAliasesMatchDatasheet(t *testing.T) {
	// Bit 1 (0x02): Index in Type I, Data Request in Type II/III.
	if stIndex != 0x02 || stDRQ != 0x02 {
		t.Errorf("bit-1 aliases wrong: Index=%02X DRQ=%02X, want 02/02", stIndex, stDRQ)
	}
	// Bit 2 (0x04): Track 0 in Type I, Lost Data in Type II/III.
	if stTrack0 != 0x04 || stLostData != 0x04 {
		t.Errorf("bit-2 aliases wrong: Track0=%02X LostData=%02X, want 04/04", stTrack0, stLostData)
	}
	// Bit 4 (0x10): Seek Error in Type I, Record Not Found in Type II/III.
	if stSeekError != 0x10 || stRNF != 0x10 {
		t.Errorf("bit-4 aliases wrong: SeekErr=%02X RNF=%02X, want 10/10", stSeekError, stRNF)
	}
	// Bit 5 (0x20): Spin-Up in Type I, Record Type in Type II/III.
	if stSpinUp != 0x20 || stRecordType != 0x20 {
		t.Errorf("bit-5 aliases wrong: SpinUp=%02X RecordType=%02X, want 20/20", stSpinUp, stRecordType)
	}
	// Bit 7 (0x80): Motor On in ALL command classes (WD1772 has no Ready line).
	if stMotorOn != 0x80 {
		t.Errorf("Motor-On bit wrong: %02X, want 80", stMotorOn)
	}
	// Fixed-meaning bits across all types.
	if stBusy != 0x01 || stCRCError != 0x08 || stWriteProtect != 0x40 {
		t.Errorf("fixed bits wrong: Busy=%02X CRC=%02X WrProt=%02X", stBusy, stCRCError, stWriteProtect)
	}
}

// ----------------------------------------------------------------------------
// Registers & the INTRQ line (datasheet "PROCESSOR INTERFACE", "Status
// Register").
// ----------------------------------------------------------------------------

// WD1772 datasheet "Status Register": "INTRQ is reset by either reading the
// Status Register or by loading the Command Register with a new command".
func TestWD1772StatusReadClearsIntrq(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsCmd(d, 0x00) // Restore raises INTRQ
	if !d.intrq {
		t.Fatal("INTRQ not raised after Restore")
	}
	dsStatus(d)
	if d.intrq {
		t.Error("INTRQ not cleared by reading the Status Register")
	}
}

// WD1772 datasheet: the Track and Sector registers are directly readable and
// writable and hold their value independently of any command.
func TestWD1772TrackSectorRegistersReadWrite(t *testing.T) {
	d := dsNewFDC(t)
	dsSetTrack(d, 0x2A)
	dsSetSector(d, 0x0C)
	if dsTrack(d) != 0x2A {
		t.Errorf("Track Register = %02X, want 2A", dsTrack(d))
	}
	if dsSector(d) != 0x0C {
		t.Errorf("Sector Register = %02X, want 0C", dsSector(d))
	}
}

// WD1772 Seek uses the Data Register as the destination track (datasheet
// "SEEK": "the Data Register contains the desired track number").
func TestWD1772DataRegisterDrivesSeek(t *testing.T) {
	d := dsNewFDC(t)
	dsSelDrive0Side0(d)
	dsWriteData(d, 55)
	dsCmd(d, 0x10) // Seek
	if dsTrack(d) != 55 {
		t.Errorf("Seek used Data Register 55, Track Register = %d, want 55", dsTrack(d))
	}
}

// ----------------------------------------------------------------------------
// Side selection through the DISCiPLE control register (port 0x1F).
// ----------------------------------------------------------------------------

// The DISCiPLE control register side-select bit routes the WD1772 to the
// requested head; a read of the same sector on side 1 vs side 0 must differ
// (the in-memory image stores the head number in byte[1]).
func TestWD1772ControlRegisterSideSelectRoutesRead(t *testing.T) {
	d := dsNewFDC(t)

	readHeadByte := func(side func(*Disciple)) byte {
		side(d)
		dsWriteData(d, 10)
		dsCmd(d, 0x10) // Seek to cylinder 10
		dsSetSector(d, 1)
		dsCmd(d, 0x80)       // Read Sector
		dsReadData(d)        // cyl
		return dsReadData(d) // head byte
	}
	side0 := readHeadByte(dsSelDrive0Side0)
	side1 := readHeadByte(dsSelDrive0Side1)
	if side0 != 0 {
		t.Errorf("side-0 read head byte = %d, want 0", side0)
	}
	if side1 != 1 {
		t.Errorf("side-1 read head byte = %d, want 1", side1)
	}
}
