package plus3fdc

// Datasheet-faithful test suite for the NEC µPD765A floppy-disk controller
// (the FDC fitted to the Spectrum +3 / +2A, driven by +3DOS).
//
// The oracle throughout is the NEC µPD765A / µPD765B datasheet
// ("Single/Double Density Floppy-Disk Controller"). Every assertion below
// cites the datasheet behaviour it pins. No emulator is used as a reference;
// these are derived purely from the published chip specification.
//
// PHASE MODEL (µPD765A datasheet, "Processor Software Interface"):
// every command moves through up to three phases —
//
//	Command   phase: the processor writes the opcode + parameter bytes.
//	Execution phase: data is transferred between FDD and processor.
//	Result    phase: the processor reads status / result bytes.
//
// MAIN STATUS REGISTER (µPD765A datasheet, "Main Status Register"):
//
//	bit  name  meaning
//	---  ----  ----------------------------------------------------------
//	 7   RQM   Request for Master — Data Register ready to transfer a byte
//	 6   DIO   Data direction — 1 = FDC→CPU, 0 = CPU→FDC
//	 5   EXM   (NDM) Execution Mode — set during a non-DMA execution phase
//	 4   CB    Controller Busy — a read/write command is in process
//	3..0 DnB   FDD n is in the Seek mode (per-drive seek-busy)
//
// Before each command/parameter byte the master must see RQM=1, DIO=0.
// Before each result byte the master must see RQM=1, DIO=1.
//
// STATUS REGISTER 0 (ST0):
//
//	7..6 IC    00 normal, 01 abnormal, 10 invalid command,
//	           11 abnormal (ready signal changed)
//	 5   SE    Seek End — set when a SEEK / RECALIBRATE completes
//	 4   EC    Equipment Check
//	 3   NR    Not Ready
//	 2   HD    Head Address
//	1..0 US    Unit Select (drive number)
//
// STATUS REGISTER 1 (ST1):
//
//	7 EN end of cylinder · 5 DE data error (CRC) · 4 OR overrun ·
//	2 ND no data · 1 NW not writable · 0 MA missing address mark
//
// STATUS REGISTER 2 (ST2):
//
//	6 CM control mark · 5 DD data error in data field · 4 WC wrong cylinder ·
//	3 SH scan equal hit · 2 SN scan not satisfied · 1 BC bad cylinder ·
//	0 MD missing address mark in data field
//
// STATUS REGISTER 3 (ST3, returned by SENSE DRIVE STATUS):
//
//	7 FT fault · 6 WP write protected · 5 RY ready · 4 T0 track 0 ·
//	3 TS two side · 2 HD head address · 1..0 US unit select

import "testing"

// ---------------------------------------------------------------------------
// In-memory disk construction.
// ---------------------------------------------------------------------------

// dsTrack builds one track byte-stream carrying the given sectors, each
// 512 bytes (N=2), with a position-dependent fill so reads can be checked
// against a value addressed by (cylinder, head, sector).
func dsTrack(t *testing.T, cyl, head byte, sectorIDs []byte) *Track {
	t.Helper()
	tr := newTrack(bytesPerTrackDD, 0xE5, cyl, head, 2)
	b := newTrackBuilder(tr, gapMFM)
	if !b.preindexAdd() || !b.postindexAdd() {
		t.Fatalf("dsTrack: track too small for index gaps")
	}
	for _, r := range sectorIDs {
		data := make([]byte, 512)
		seed := byte(int(cyl)*7 + int(head)*131 + int(r)*17)
		for i := range data {
			data[i] = seed + byte(i)
		}
		if !b.idAdd(cyl, head, r, 2, false) {
			t.Fatalf("dsTrack: track full at sector %d", r)
		}
		if _, ok := b.dataAdd(data, false, false); !ok {
			t.Fatalf("dsTrack: no room for sector %d data", r)
		}
	}
	if !b.gap4Add() {
		t.Fatalf("dsTrack: track too small for GAP IV")
	}
	return tr
}

// dsSeed returns the first data byte the position-dependent fill puts at
// offset 0 of a sector — i.e. the value dsTrack seeds (c,h,r) with.
func dsSeed(cyl, head, r byte) byte {
	return byte(int(cyl)*7 + int(head)*131 + int(r)*17)
}

// dsDisk builds a small 2-side, 2-cylinder disk with sectors 1..9 per
// track, addressable through the µPD765 read/seek/sense path.
func dsDisk(t *testing.T) *Disk {
	t.Helper()
	const cyls, sides = 2, 2
	d := &Disk{Sides: sides, Cylinders: cyls, Tracks: make([][]*Track, sides)}
	ids := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	for h := 0; h < sides; h++ {
		d.Tracks[h] = make([]*Track, cyls)
		for c := 0; c < cyls; c++ {
			d.Tracks[h][c] = dsTrack(t, byte(c), byte(h), ids)
		}
	}
	return d
}

// ---------------------------------------------------------------------------
// Phase / handshake helpers.
// ---------------------------------------------------------------------------

// dsCommand writes an opcode + parameter bytes, checking RQM=1/DIO=0
// (master-may-write) before each byte exactly as the datasheet requires.
func dsCommand(t *testing.T, f *UPD765, bytes ...byte) {
	t.Helper()
	for i, b := range bytes {
		st := f.ReadStatus()
		if st&msrRQM == 0 {
			t.Fatalf("command byte %d: RQM clear (MSR=%02X); FDC not ready", i, st)
		}
		if st&msrDIO != 0 {
			t.Fatalf("command byte %d: DIO set (MSR=%02X); FDC wants to send, not receive", i, st)
		}
		f.WriteData(b)
	}
}

// dsResult reads n result bytes, checking RQM=1/DIO=1/CB=1 before each, and
// that the FDC returns to the idle command state afterwards.
func dsResult(t *testing.T, f *UPD765, n int) []byte {
	t.Helper()
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		st := f.ReadStatus()
		if st&msrRQM == 0 {
			t.Fatalf("result byte %d: RQM clear (MSR=%02X)", i, st)
		}
		if st&msrDIO == 0 {
			t.Fatalf("result byte %d: DIO clear (MSR=%02X); FDC should be sending", i, st)
		}
		if st&msrCB == 0 {
			t.Fatalf("result byte %d: CB clear (MSR=%02X); controller should still be busy", i, st)
		}
		out = append(out, f.ReadData())
	}
	if st := f.ReadStatus(); st != msrRQM {
		t.Fatalf("after result phase: MSR=%02X, want %02X (idle, RQM only)", st, msrRQM)
	}
	return out
}

// ---------------------------------------------------------------------------
// Main Status Register transitions (µPD765A datasheet, "Main Status
// Register" + the command/execution/result phase flow charts).
// ---------------------------------------------------------------------------

// At reset / idle the FDC is ready to accept a command: RQM=1, DIO=0, CB=0.
// (µPD765A datasheet: the master polls MSR for RQM=1 before issuing a
// command; the controller is not busy until the opcode is latched.)
func TestDSIdleMSR(t *testing.T) {
	f := NewUPD765()
	if st := f.ReadStatus(); st != msrRQM {
		t.Fatalf("idle MSR=%02X, want %02X (RQM only)", st, msrRQM)
	}
	if st := f.ReadStatus(); st&msrCB != 0 {
		t.Errorf("idle MSR CB set (%02X); no command in process", st)
	}
	if st := f.ReadStatus(); st&msrDIO != 0 {
		t.Errorf("idle MSR DIO set (%02X); FDC should expect a write", st)
	}
}

// Once the opcode and at least one parameter byte are latched, CB goes high
// (a command is in process) while the FDC continues to collect parameters
// with DIO=0. (µPD765A datasheet command-phase handshake.)
func TestDSCommandPhaseCBRaised(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))
	// SEEK opcode (0x0F) needs 2 parameters; after the opcode + first
	// parameter the controller is mid-command.
	f.WriteData(0x0F)
	f.WriteData(0x00) // HD/US
	st := f.ReadStatus()
	if st&msrCB == 0 {
		t.Errorf("mid-command MSR=%02X: CB clear, want set", st)
	}
	if st&msrRQM == 0 {
		t.Errorf("mid-command MSR=%02X: RQM clear, FDC should want the next byte", st)
	}
	if st&msrDIO != 0 {
		t.Errorf("mid-command MSR=%02X: DIO set, want clear (collecting params)", st)
	}
}

// During a READ DATA execution phase the FDC sends data to the processor:
// MSR must read RQM=1, DIO=1 (FDC→CPU), EXM=1 (non-DMA execution), CB=1.
// (µPD765A datasheet, "Execution Phase" in non-DMA mode.)
func TestDSReadExecutionPhaseMSR(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x46, 0x00, 0x00, 0x00, 0x01, 0x02, 0x09, 0x2A, 0xFF)
	st := f.ReadStatus()
	const want = msrRQM | msrDIO | msrEXM | msrCB
	if st != want {
		t.Fatalf("READ execution MSR=%02X, want %02X (RQM|DIO|EXM|CB)", st, want)
	}
}

// During a WRITE DATA execution phase the FDC receives data from the
// processor: MSR must read RQM=1, DIO=0 (CPU→FDC), EXM=1, CB=1.
// (µPD765A datasheet, "Execution Phase" — DIO direction is reversed for a
// write.)
func TestDSWriteExecutionPhaseMSR(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x45, 0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0x2A, 0xFF)
	st := f.ReadStatus()
	const want = msrRQM | msrEXM | msrCB // DIO clear: master writes
	if st != want {
		t.Fatalf("WRITE execution MSR=%02X, want %02X (RQM|EXM|CB, DIO clear)", st, want)
	}
}

// In the result phase the FDC presents result bytes: RQM=1, DIO=1, CB=1,
// and EXM is NOT set (the execution phase has ended). (µPD765A datasheet,
// "Result Phase".)
func TestDSResultPhaseMSR(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	// SENSE DRIVE STATUS produces a single-byte result phase immediately.
	dsCommand(t, f, 0x04, 0x00)
	st := f.ReadStatus()
	if st&(msrRQM|msrDIO|msrCB) != (msrRQM | msrDIO | msrCB) {
		t.Errorf("result MSR=%02X: want RQM|DIO|CB all set", st)
	}
	if st&msrEXM != 0 {
		t.Errorf("result MSR=%02X: EXM set, want clear (no execution phase)", st)
	}
	f.ReadData()
}

// ---------------------------------------------------------------------------
// SPECIFY (µPD765A datasheet): 3 command bytes (opcode + SRT/HUT + HLT/ND),
// no result phase — the FDC returns straight to the idle command state.
// ---------------------------------------------------------------------------

func TestDSSpecifyNoResult(t *testing.T) {
	f := NewUPD765()
	// SRT=12 HUT=15 -> 0xCF ; HLT=1 ND=0 -> 0x02
	dsCommand(t, f, 0x03, 0xCF, 0x02)
	if st := f.ReadStatus(); st != msrRQM {
		t.Fatalf("after SPECIFY: MSR=%02X, want %02X (idle, no result)", st, msrRQM)
	}
}

// ---------------------------------------------------------------------------
// RECALIBRATE + SENSE INTERRUPT STATUS (µPD765A datasheet).
// RECALIBRATE: 2 command bytes (opcode + US), no result phase; the head is
// driven to track 0 and PCN becomes 0. A SENSE INTERRUPT STATUS afterwards
// returns ST0 (with SE set) and PCN.
// ---------------------------------------------------------------------------

func TestDSRecalibrateToTrack0(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))
	f.pcn[0] = 1 // park away from track 0

	dsCommand(t, f, 0x07, 0x00) // RECALIBRATE drive 0
	if st := f.ReadStatus(); st != msrRQM {
		t.Fatalf("after RECALIBRATE: MSR=%02X, want idle (no result phase)", st)
	}
	if f.pcn[0] != 0 {
		t.Fatalf("RECALIBRATE: PCN=%d, want 0", f.pcn[0])
	}

	dsCommand(t, f, 0x08) // SENSE INTERRUPT STATUS
	res := dsResult(t, f, 2)
	// ST0: SE set (bit5), IC=00 normal, US=0.
	if res[0]&st0SE == 0 {
		t.Errorf("SENSE INT ST0=%02X: SE clear, want set", res[0])
	}
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("SENSE INT ST0=%02X: IC nonzero, want normal (drive ready)", res[0])
	}
	if res[0]&0x03 != 0 {
		t.Errorf("SENSE INT ST0=%02X: US bits nonzero, want drive 0", res[0])
	}
	if res[1] != 0 {
		t.Errorf("SENSE INT PCN=%d, want 0", res[1])
	}
}

// ---------------------------------------------------------------------------
// SEEK + SENSE INTERRUPT STATUS (µPD765A datasheet).
// SEEK: 3 command bytes (opcode + HD/US + NCN), no result phase. The PCN
// advances to NCN; a following SENSE INTERRUPT returns ST0(SE) and the new
// PCN.
// ---------------------------------------------------------------------------

func TestDSSeekThenSenseInterrupt(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x0F, 0x00, 0x01) // SEEK drive 0 to cyl 1
	if st := f.ReadStatus(); st != msrRQM {
		t.Fatalf("after SEEK: MSR=%02X, want idle (no result phase)", st)
	}
	if f.pcn[0] != 1 {
		t.Fatalf("SEEK: PCN=%d, want 1", f.pcn[0])
	}

	dsCommand(t, f, 0x08) // SENSE INTERRUPT STATUS
	res := dsResult(t, f, 2)
	if res[0]&st0SE == 0 {
		t.Errorf("SENSE INT ST0=%02X: SE clear, want set after SEEK", res[0])
	}
	if res[1] != 1 {
		t.Errorf("SENSE INT PCN=%d, want 1 (the NCN we seeked to)", res[1])
	}
}

// SENSE INTERRUPT STATUS issued with no pending seek interrupt is an
// invalid request: the µPD765A returns ST0 = 0x80 (IC=10, invalid command)
// in a single-byte result phase. (Datasheet: "if a Sense Interrupt Status
// command is issued when no interrupt has occurred, ST0 = 80h is returned".)
func TestDSSenseInterruptNoPending(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x08)
	res := dsResult(t, f, 1)
	if res[0] != 0x80 {
		t.Errorf("SENSE INT with no pending seek: ST0=%02X, want 0x80 (IC=invalid)", res[0])
	}
}

// A SEEK to a drive with no disk fitted terminates abnormally: the SENSE
// INTERRUPT STATUS ST0 carries NR (not ready, bit3) and IC=01 abnormal.
// (µPD765A datasheet: not-ready FDD → ST0 NR + abnormal IC.)
func TestDSSeekNotReadyDrive(t *testing.T) {
	f := NewUPD765()
	// No disk attached to drive 0.
	dsCommand(t, f, 0x0F, 0x00, 0x05)
	dsCommand(t, f, 0x08)
	res := dsResult(t, f, 2)
	if res[0]&st0NR == 0 {
		t.Errorf("SEEK on empty drive: ST0=%02X, NR clear, want set", res[0])
	}
	if res[0]&st0IC0 == 0 {
		t.Errorf("SEEK on empty drive: ST0=%02X, IC0 clear, want abnormal", res[0])
	}
}

// ---------------------------------------------------------------------------
// SENSE DRIVE STATUS (µPD765A datasheet): 2 command bytes (opcode +
// HD/US), 1 result byte = ST3. The bits report the live drive lines.
// ---------------------------------------------------------------------------

func TestDSSenseDriveStatusBits(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t)) // 2-sided disk, head at track 0

	dsCommand(t, f, 0x04, 0x00) // HD=0 US=0
	res := dsResult(t, f, 1)
	st3 := res[0]
	// RY (bit5): a disk is fitted → ready.
	if st3&st3RY == 0 {
		t.Errorf("ST3=%02X: RY clear, want set (disk present)", st3)
	}
	// T0 (bit4): PCN=0 → on track 0.
	if st3&st3T0 == 0 {
		t.Errorf("ST3=%02X: T0 clear, want set (PCN=0)", st3)
	}
	// TS (bit3): the test disk is double-sided.
	if st3&st3TS == 0 {
		t.Errorf("ST3=%02X: TS clear, want set (2-sided disk)", st3)
	}
	// WP (bit6): default writable.
	if st3&st3WP != 0 {
		t.Errorf("ST3=%02X: WP set, want clear (writable by default)", st3)
	}
	// US (bits1-0): drive 0.
	if st3&0x03 != 0 {
		t.Errorf("ST3=%02X: US nonzero, want drive 0", st3)
	}
}

// SENSE DRIVE STATUS reflects the head-address (HD) bit from the command,
// and the T0 bit drops once the head is off track 0. (µPD765A datasheet
// ST3 HD bit2 = side-select output; T0 bit4 = track-0 line.)
func TestDSSenseDriveStatusHeadAndTrack(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))
	f.pcn[0] = 1 // off track 0

	dsCommand(t, f, 0x04, 0x04) // HD=1 US=0
	res := dsResult(t, f, 1)
	st3 := res[0]
	if st3&st3HD == 0 {
		t.Errorf("ST3=%02X: HD clear, want set (commanded head 1)", st3)
	}
	if st3&st3T0 != 0 {
		t.Errorf("ST3=%02X: T0 set, want clear (PCN=1, off track 0)", st3)
	}
}

// SENSE DRIVE STATUS on an empty drive reports RY clear. (µPD765A
// datasheet ST3 RY = ready line, low when no disk.)
func TestDSSenseDriveStatusNotReady(t *testing.T) {
	f := NewUPD765()
	// Nothing attached.
	dsCommand(t, f, 0x04, 0x00)
	res := dsResult(t, f, 1)
	if res[0]&st3RY != 0 {
		t.Errorf("empty drive ST3=%02X: RY set, want clear", res[0])
	}
}

// ---------------------------------------------------------------------------
// READ DATA (µPD765A datasheet): 9 command bytes
// (opcode, HD/US, C, H, R, N, EOT, GPL, DTL), an execution phase that
// streams 128<<N data bytes, then a 7-byte result phase
// (ST0, ST1, ST2, C, H, R, N).
// ---------------------------------------------------------------------------

func TestDSReadDataKnownSector(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	// READ DATA (MFM, 0x46): cyl 0, head 0, R=3, N=2, EOT=3.
	// This is a single-sector read (R == EOT). The 512 data bytes stream
	// out, then a 7-byte result phase.
	dsCommand(t, f, 0x46, 0x00, 0x00, 0x00, 0x03, 0x02, 0x03, 0x2A, 0xFF)

	got := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		st := f.ReadStatus()
		if st&msrRQM == 0 || st&msrDIO == 0 || st&msrEXM == 0 {
			t.Fatalf("exec byte %d MSR=%02X: want RQM|DIO|EXM", i, st)
		}
		got = append(got, f.ReadData())
	}

	// The dsTrack fill makes byte i of sector (0,0,3) == seed + i.
	seed := dsSeed(0, 0, 3)
	for i := 0; i < 512; i++ {
		if got[i] != seed+byte(i) {
			t.Fatalf("READ DATA byte %d = %02X, want %02X", i, got[i], seed+byte(i))
		}
	}

	res := dsResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("READ DATA ST0=%02X: IC nonzero, want normal", res[0])
	}
	// ST2 must be clean: no CRC / wrong-cylinder / control-mark errors.
	if res[2] != 0 {
		t.Errorf("READ DATA ST2=%02X: want 0 (no data error)", res[2])
	}
	// ST1.EN (bit7): the read reached EOT (R==EOT==3), so the FDC has
	// accessed the final sector of the cylinder and sets End-of-Cylinder.
	// (µPD765A datasheet: EN is set when the FDC tries to access a sector
	// beyond the final sector of a cylinder — i.e. it increments R past
	// EOT after the last sector.)
	if res[1] != st1EN {
		t.Errorf("READ DATA ST1=%02X: want EN (0x80) only for the EOT sector", res[1])
	}
	// C/H/R/N result fields (µPD765A datasheet: on a successful read R is
	// incremented to point past the last sector transferred).
	if res[3] != 0 || res[4] != 0 {
		t.Errorf("READ DATA result C=%d H=%d: want 0,0", res[3], res[4])
	}
	if res[5] != 4 {
		t.Errorf("READ DATA result R=%d: want 4 (R incremented past 3)", res[5])
	}
	if res[6] != 2 {
		t.Errorf("READ DATA result N=%d: want 2", res[6])
	}
}

// READ DATA for a sector ID that isn't on the track terminates abnormally:
// ST0 IC=01 and ST1.ND (no data / sector not found). (µPD765A datasheet:
// the FDC cannot find the requested sector after two index pulses → ND.)
func TestDSReadDataSectorNotFound(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x46, 0x00, 0x00, 0x00, 0x63, 0x02, 0x63, 0x2A, 0xFF) // R=0x63
	res := dsResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("missing sector ST0=%02X: IC0 clear, want abnormal", res[0])
	}
	if res[1]&st1ND == 0 {
		t.Errorf("missing sector ST1=%02X: ND clear, want set", res[1])
	}
}

// READ DATA against an empty drive: NR is reported in ST0, the command
// terminates abnormally, and the requested CHRN is echoed in the result.
// (µPD765A datasheet not-ready handling.)
func TestDSReadDataNotReady(t *testing.T) {
	f := NewUPD765()
	// No disk.
	dsCommand(t, f, 0x46, 0x00, 0x00, 0x00, 0x01, 0x02, 0x01, 0x2A, 0xFF)
	res := dsResult(t, f, 7)
	if res[0]&st0NR == 0 {
		t.Errorf("empty-drive READ ST0=%02X: NR clear, want set", res[0])
	}
	if res[0]&st0IC0 == 0 {
		t.Errorf("empty-drive READ ST0=%02X: IC0 clear, want abnormal", res[0])
	}
}

// READ DATA whose C parameter doesn't match the cylinder recorded in the
// sector ID sets ST2.WC (wrong cylinder) and terminates abnormally.
// (µPD765A datasheet ST2 WC bit4.)
func TestDSReadDataWrongCylinder(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	// Track at cyl 0 records C=0 in its IDs, but we command C=5.
	dsCommand(t, f, 0x46, 0x00, 0x05, 0x00, 0x01, 0x02, 0x01, 0x2A, 0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := dsResult(t, f, 7)
	if res[2]&st2WC == 0 {
		t.Errorf("wrong-cylinder ST2=%02X: WC clear, want set", res[2])
	}
	if res[0]&st0IC0 == 0 {
		t.Errorf("wrong-cylinder ST0=%02X: IC0 clear, want abnormal", res[0])
	}
}

// Multi-sector READ DATA: with EOT > R the FDC reads consecutive sectors in
// one execution phase. On reaching the final sector (R==EOT) it sets ST1.EN
// (end of cylinder) and the result R is EOT+1. (µPD765A datasheet, multi-
// sector transfer + EN bit.)
func TestDSReadDataMultiSectorEndOfCylinder(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	// READ DATA cyl 0 head 0, R=1, EOT=3 → reads sectors 1,2,3.
	dsCommand(t, f, 0x46, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x2A, 0xFF)

	got := make([]byte, 0, 3*512)
	for i := 0; i < 3*512; i++ {
		got = append(got, f.ReadData())
	}
	res := dsResult(t, f, 7)

	for s := byte(1); s <= 3; s++ {
		seed := dsSeed(0, 0, s)
		base := int(s-1) * 512
		if got[base] != seed {
			t.Errorf("multi-sector: sector %d first byte = %02X, want %02X", s, got[base], seed)
		}
	}
	if res[1]&st1EN == 0 {
		t.Errorf("multi-sector ST1=%02X: EN clear, want set at end of cylinder", res[1])
	}
	if res[5] != 4 {
		t.Errorf("multi-sector result R=%d, want 4 (EOT 3 + 1)", res[5])
	}
}

// ---------------------------------------------------------------------------
// READ ID (µPD765A datasheet): 2 command bytes (opcode + HD/US). The FDC
// returns the first ID field it encounters in a 7-byte result phase
// (ST0, ST1, ST2, C, H, R, N). Successive READ IDs walk the track.
// ---------------------------------------------------------------------------

func TestDSReadIDReturnsFirstSector(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x4A, 0x00) // READ ID (MFM)
	res := dsResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("READ ID ST0=%02X: IC nonzero, want normal", res[0])
	}
	if res[3] != 0 || res[4] != 0 {
		t.Errorf("READ ID C=%d H=%d: want 0,0", res[3], res[4])
	}
	if res[5] != 1 {
		t.Errorf("READ ID R=%d: want first sector 1", res[5])
	}
	if res[6] != 2 {
		t.Errorf("READ ID N=%d: want 2", res[6])
	}
}

// On a drive with no disk, READ ID terminates abnormally with ST0.NR/IC and
// ST1.MA|ND. (µPD765A datasheet: no medium → cannot find an address mark.)
func TestDSReadIDNotReady(t *testing.T) {
	f := NewUPD765()
	dsCommand(t, f, 0x4A, 0x00)
	res := dsResult(t, f, 7)
	if res[0]&st0NR == 0 {
		t.Errorf("READ ID empty drive ST0=%02X: NR clear, want set", res[0])
	}
	if res[1]&(st1MA|st1ND) == 0 {
		t.Errorf("READ ID empty drive ST1=%02X: want MA/ND set", res[1])
	}
}

// ---------------------------------------------------------------------------
// FORMAT TRACK / WRITE ID (µPD765A datasheet): 6 command bytes
// (opcode, HD/US, N, SC, GPL, D), then SC×4 (C,H,R,N) bytes in the
// execution phase, then a 7-byte result. The new IDs become readable.
// ---------------------------------------------------------------------------

func TestDSFormatTrackLaysSectors(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	// FORMAT TRACK (MFM, 0x4D): N=2, SC=3, GPL=0x54, filler=0xE5.
	dsCommand(t, f, 0x4D, 0x00, 0x02, 0x03, 0x54, 0xE5)
	// CHRN for 3 sectors, IDs 5,6,7.
	for _, s := range [][4]byte{{0, 0, 5, 2}, {0, 0, 6, 2}, {0, 0, 7, 2}} {
		for _, b := range s {
			f.WriteData(b)
		}
	}
	res := dsResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("FORMAT ST0=%02X: IC nonzero, want normal", res[0])
	}

	// READ ID should now find the freshly-formatted IDs in order.
	want := []byte{5, 6, 7}
	for i := 0; i < 3; i++ {
		dsCommand(t, f, 0x4A, 0x00)
		r := dsResult(t, f, 7)
		if r[5] != want[i] {
			t.Errorf("formatted READ ID %d: R=%d, want %d", i, r[5], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Invalid command (µPD765A datasheet): an unrecognised opcode produces a
// single-byte result phase with ST0 = 0x80 (IC bits 7-6 = 10, "invalid
// command issued") and the controller returns to idle.
// ---------------------------------------------------------------------------

func TestDSInvalidCommand(t *testing.T) {
	f := NewUPD765()
	// 0x00 is not a valid µPD765 opcode.
	dsCommand(t, f, 0x00)
	res := dsResult(t, f, 1)
	if res[0] != 0x80 {
		t.Errorf("invalid command ST0=%02X, want 0x80 (IC=invalid)", res[0])
	}
}

// Several reserved opcodes all decode to the same invalid response.
// (µPD765A datasheet: opcodes 0x00, 0x01, 0x12, 0x16, 0x17, 0x1A, etc. are
// undefined.)
func TestDSInvalidCommandVariants(t *testing.T) {
	for _, op := range []byte{0x00, 0x01, 0x16, 0x17, 0x18, 0x1B, 0x1C, 0x1E} {
		f := NewUPD765()
		dsCommand(t, f, op)
		res := dsResult(t, f, 1)
		if res[0] != 0x80 {
			t.Errorf("invalid opcode %02X: ST0=%02X, want 0x80", op, res[0])
		}
	}
}

// ---------------------------------------------------------------------------
// Drive addressing: the µPD765 US1/US0 bits select the unit, and the
// selected unit's number is echoed in the ST0/ST3 US field.
// (µPD765A datasheet ST0/ST3 bits 1-0.) On the +3 only US0 is wired, so
// US1 is folded away — but the datasheet US echo for the addressed unit is
// what we pin here for drive 1 (US0=1).
// ---------------------------------------------------------------------------

func TestDSDriveSelectEchoedInST0(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(1, dsDisk(t)) // drive B

	// SEEK drive 1 (US0=1) to cyl 1, then SENSE INTERRUPT.
	dsCommand(t, f, 0x0F, 0x01, 0x01)
	dsCommand(t, f, 0x08)
	res := dsResult(t, f, 2)
	if res[0]&0x01 == 0 {
		t.Errorf("SENSE INT ST0=%02X: US0 clear, want drive 1 selected", res[0])
	}
	if res[1] != 1 {
		t.Errorf("SENSE INT PCN=%d, want 1", res[1])
	}
}

// ---------------------------------------------------------------------------
// SEEK leaves a per-drive Seek-mode busy bit that SENSE INTERRUPT clears.
// After the SENSE INTERRUPT acknowledges the seek, a second SENSE INTERRUPT
// with nothing pending returns the invalid ST0=0x80. (µPD765A datasheet:
// the interrupt is reset by reading ST0 via SENSE INTERRUPT STATUS.)
// ---------------------------------------------------------------------------

func TestDSSenseInterruptClearsPending(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x0F, 0x00, 0x01) // SEEK
	dsCommand(t, f, 0x08)             // first SENSE INT — picks up the seek
	dsResult(t, f, 2)

	dsCommand(t, f, 0x08) // second SENSE INT — nothing pending now
	res := dsResult(t, f, 1)
	if res[0] != 0x80 {
		t.Errorf("second SENSE INT ST0=%02X, want 0x80 (no interrupt pending)", res[0])
	}
}

// ---------------------------------------------------------------------------
// Full sequence smoke test mirroring how +3DOS reads a sector:
// SPECIFY → RECALIBRATE → SENSE INT → SEEK → SENSE INT → READ ID →
// READ DATA. Pins that the phase model survives a realistic command stream.
// ---------------------------------------------------------------------------

func TestDSTypicalLoadSequence(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, dsDisk(t))

	dsCommand(t, f, 0x03, 0xCF, 0x02) // SPECIFY
	dsCommand(t, f, 0x07, 0x00)       // RECALIBRATE
	dsCommand(t, f, 0x08)             // SENSE INT
	if pcn := dsResult(t, f, 2)[1]; pcn != 0 {
		t.Fatalf("after RECALIBRATE PCN=%d, want 0", pcn)
	}

	dsCommand(t, f, 0x0F, 0x00, 0x01) // SEEK cyl 1
	dsCommand(t, f, 0x08)             // SENSE INT
	if pcn := dsResult(t, f, 2)[1]; pcn != 1 {
		t.Fatalf("after SEEK PCN=%d, want 1", pcn)
	}

	dsCommand(t, f, 0x4A, 0x00) // READ ID on cyl 1
	idres := dsResult(t, f, 7)
	if idres[3] != 1 {
		t.Fatalf("READ ID C=%d, want 1 (we seeked to cyl 1)", idres[3])
	}

	// READ DATA the sector READ ID just reported.
	r := idres[5]
	dsCommand(t, f, 0x46, 0x00, 0x01, 0x00, r, 0x02, r, 0x2A, 0xFF)
	seed := dsSeed(1, 0, r)
	for i := 0; i < 512; i++ {
		b := f.ReadData()
		if b != seed+byte(i) {
			t.Fatalf("load READ DATA byte %d = %02X, want %02X", i, b, seed+byte(i))
		}
	}
	res := dsResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("load READ DATA ST0=%02X: IC nonzero, want normal", res[0])
	}
}
