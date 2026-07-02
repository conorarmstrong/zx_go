package plus3fdc

import (
	"testing"
)

// loadTestDisk parses the standard synthetic DSK and returns it.
func loadTestDisk(t *testing.T) *Disk {
	t.Helper()
	data, _ := buildSyntheticDSK(t, false)
	d, err := ParseDSK(data)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	return d
}

// drainResult reads MSR and the result phase bytes, returning them.
// The FDC must already be in result phase.
func drainResult(t *testing.T, f *UPD765, expectLen int) []byte {
	t.Helper()
	out := make([]byte, 0, expectLen)
	for i := 0; i < expectLen; i++ {
		st := f.ReadStatus()
		if st&msrRQM == 0 {
			t.Fatalf("result byte %d: RQM clear (status=%02X)", i, st)
		}
		if st&msrDIO == 0 {
			t.Fatalf("result byte %d: DIO clear (status=%02X)", i, st)
		}
		if st&msrCB == 0 {
			t.Fatalf("result byte %d: CB clear (status=%02X)", i, st)
		}
		out = append(out, f.ReadData())
	}
	// After the last result byte the FDC should be back to idle.
	if st := f.ReadStatus(); st != msrRQM {
		t.Errorf("after result phase: status=%02X, want %02X", st, msrRQM)
	}
	return out
}

func TestUPD765Specify(t *testing.T) {
	f := NewUPD765()
	// SPECIFY 03 SRT/HUT HLT/ND — should leave FDC idle with no result.
	f.WriteData(0x03)
	f.WriteData(0xAF)
	f.WriteData(0x02)
	if st := f.ReadStatus(); st != msrRQM {
		t.Errorf("after SPECIFY: status=%02X, want %02X (idle)", st, msrRQM)
	}
}

func TestUPD765RecalibrateThenSenseInt(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// Move PCN away from zero so RECALIBRATE has somewhere to come from.
	f.pcn[0] = 23

	// RECALIBRATE drive 0
	f.WriteData(0x07)
	f.WriteData(0x00)
	if f.pcn[0] != 0 {
		t.Errorf("after RECALIBRATE: pcn[0]=%d, want 0", f.pcn[0])
	}

	// SENSE INTERRUPT — expect ST0 with SE bit set, and PCN=0.
	f.WriteData(0x08)
	res := drainResult(t, f, 2)
	if res[0]&st0SE == 0 {
		t.Errorf("SENSE INT ST0 = %02X, expected SE bit set", res[0])
	}
	if res[1] != 0 {
		t.Errorf("SENSE INT PCN = %d, want 0", res[1])
	}
}

func TestUPD765SeekThenSenseInt(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SEEK drive 0, NCN=12
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(12)
	if f.pcn[0] != 12 {
		t.Errorf("after SEEK: pcn[0]=%d, want 12", f.pcn[0])
	}

	// SENSE INTERRUPT — expect ST0 SE set, PCN=12.
	f.WriteData(0x08)
	res := drainResult(t, f, 2)
	if res[0]&st0SE == 0 {
		t.Errorf("SENSE INT ST0 = %02X, expected SE", res[0])
	}
	if res[1] != 12 {
		t.Errorf("SENSE INT PCN = %d, want 12", res[1])
	}
}

func TestUPD765SenseDriveStatus(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SENSE DRIVE STATUS for drive 0 head 0 — expect RY, T0, TS set.
	// WP defaults to false for drive A.
	f.WriteData(0x04)
	f.WriteData(0x00) // HD=0 US=0
	res := drainResult(t, f, 1)
	st3 := res[0]
	if st3&st3RY == 0 {
		t.Errorf("ST3=%02X: RY clear, want set (drive 0 has disk)", st3)
	}
	if st3&st3T0 == 0 {
		t.Errorf("ST3=%02X: T0 clear, want set (PCN=0)", st3)
	}
	if st3&st3TS == 0 {
		t.Errorf("ST3=%02X: TS clear, want set (synthetic disk is 2-sided)", st3)
	}
	if st3&st3WP != 0 {
		t.Errorf("ST3=%02X: WP set, want clear (default writable)", st3)
	}
}

func TestUPD765SenseDriveWriteProtect(t *testing.T) {
	// Toggle WP and confirm SENSE DRIVE reflects it.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetWriteProtect(0, true)

	f.WriteData(0x04)
	f.WriteData(0x00)
	res := drainResult(t, f, 1)
	if res[0]&st3WP == 0 {
		t.Errorf("ST3=%02X: WP clear after SetWriteProtect(0, true)", res[0])
	}
}

func TestUPD765WriteDataRoundTrip(t *testing.T) {
	// Write a recognisable pattern into a sector, then read it back and
	// verify the bytes match. This is the full write→read roundtrip for
	// the in-memory disk.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// WRITE DATA cmd: 0x45 (MFM + WRITE DATA), 8 params.
	f.WriteData(0x45)
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	// Execution phase — send 512 bytes of data.
	want := make([]byte, 512)
	for i := range want {
		want[i] = byte(i ^ 0x5A)
	}
	for _, b := range want {
		f.WriteData(b)
	}

	// Drain result phase.
	res := drainResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("WRITE DATA result ST0=%02X: IC bits set, want normal", res[0])
	}

	// Now READ DATA for the same sector and check the bytes.
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	got := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		got = append(got, f.ReadData())
	}
	drainResult(t, f, 7)

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte %d: got %02X, want %02X", i, got[i], want[i])
			break
		}
	}
}

func TestUPD765WriteProtectBlocksWrite(t *testing.T) {
	// With WP set, WRITE DATA should fail immediately with ST1.NW set
	// and never enter execution phase.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetWriteProtect(0, true)

	f.WriteData(0x45)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	res := drainResult(t, f, 7)
	if res[1]&0x02 == 0 {
		t.Errorf("write-protected WRITE DATA ST1=%02X: NW clear, want set", res[1])
	}
	if res[0]&st0IC0 == 0 {
		t.Errorf("write-protected WRITE DATA ST0=%02X: IC0 clear, want abnormal", res[0])
	}
}

func TestUPD765WriteDeletedDataSetsCM(t *testing.T) {
	// WRITE DELETED DATA followed by READ DATA should report ST2.CM.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x49) // WRITE DELETED DATA (0x09 | 0x40 MFM)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.WriteData(0xAA)
	}
	drainResult(t, f, 7)

	// Read it back — ST2.CM should be set.
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x40 == 0 {
		t.Errorf("after WRITE DELETED DATA, read ST2=%02X: CM clear, want set", res[2])
	}
}

func TestUPD765FormatTrack(t *testing.T) {
	// FORMAT TRACK with 5 sectors; verify all 5 IDAMs show up in the
	// right physical order with the requested CHRN values.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SEEK to cyl 7 first.
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(7)
	f.WriteData(0x08) // SENSE INT to drain seek
	drainResult(t, f, 2)

	// WRITE ID cmd: 0x4D (0x0D | 0x40 MFM), 5 params.
	f.WriteData(0x4D)
	f.WriteData(0x00) // HD=0 US=0
	f.WriteData(0x02) // N = 2 (512 bytes)
	f.WriteData(0x05) // SC = 5 sectors
	f.WriteData(0x54) // GPL
	f.WriteData(0xE5) // filler

	// Execution phase: send 5*4 = 20 CHRN bytes.
	sectors := [][4]byte{
		{7, 0, 1, 2},
		{7, 0, 4, 2},
		{7, 0, 2, 2},
		{7, 0, 5, 2},
		{7, 0, 3, 2},
	}
	for _, s := range sectors {
		for _, b := range s {
			f.WriteData(b)
		}
	}
	res := drainResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("FORMAT TRACK result ST0=%02X: IC bits set, want normal", res[0])
	}

	// Walk the track with READ ID and confirm all 5 IDAMs are present
	// in the order we formatted them.
	got := []byte{}
	for i := 0; i < 5; i++ {
		f.WriteData(0x4A)
		f.WriteData(0x00)
		res := drainResult(t, f, 7)
		got = append(got, res[5])
	}
	want := []byte{1, 4, 2, 5, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("formatted sector %d: R=%d, want %d (full order %v)", i, got[i], want[i], got)
			break
		}
	}
}

func TestPlus3FDCPortDecode(t *testing.T) {
	// The +3 hardware decodes the FDC at 0x2FFD (status, read-only)
	// and 0x3FFD (data, read/write) using the mask 0xF002 / value
	// 0x2000 or 0x3000. Verify the decoder accepts the canonical
	// addresses, accepts aliased ports that hit the same mask, and
	// rejects ports that don't match.
	p := New()
	p.fdc.AttachDisk(0, loadTestDisk(t))

	// Status port aliases: any port where (p & 0xF002) == 0x2000.
	statusPorts := []uint16{0x2FFD, 0x2FFC, 0x2000, 0x2001, 0x2FF0}
	for _, port := range statusPorts {
		if _, handled := p.HandlePortRead(port); !handled {
			t.Errorf("status port %04X: not handled", port)
		}
	}

	// Data port aliases: any port where (p & 0xF002) == 0x3000.
	dataPorts := []uint16{0x3FFD, 0x3FFC, 0x3000, 0x3001, 0x3FF0}
	for _, port := range dataPorts {
		if _, handled := p.HandlePortRead(port); !handled {
			t.Errorf("data port %04X: not handled", port)
		}
	}

	// Ports that should NOT match: bit 1 set (status), or wrong
	// bits 15-12.
	rejectPorts := []uint16{0x2FFF, 0x3FFF, 0x2002, 0x3002, 0x0FFD, 0x1FFD, 0x4FFD, 0x6FFD, 0xAFFD}
	for _, port := range rejectPorts {
		if _, handled := p.HandlePortRead(port); handled {
			t.Errorf("port %04X: handled but should not be", port)
		}
	}

	// Writes to the status port are accepted (handled=true) so the
	// floating bus doesn't kick in, even though the FDC ignores them.
	if !p.HandlePortWrite(0x2FFD, 0xFF) {
		t.Errorf("status-port write: not handled")
	}
	// Writes to the data port should also be handled.
	if !p.HandlePortWrite(0x3FFD, 0x03) {
		t.Errorf("data-port write: not handled")
	}
}

func TestUPD765FormatThenReadDataRoundTrip(t *testing.T) {
	// FORMAT TRACK with 5 sectors and a recognisable filler, then
	// READ DATA each sector and verify the bytes come back as the
	// filler. The existing FORMAT TRACK test only walks IDAMs via
	// READ ID; this test catches off-by-one errors in DAM placement,
	// CRC priming for freshly-formatted sectors, and the WRITE ID
	// staging swap.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SEEK to cyl 12 first.
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(12)
	f.WriteData(0x08)
	drainResult(t, f, 2)

	// FORMAT TRACK with 4 sectors of 512 bytes, filler 0xC3.
	const filler = byte(0xC3)
	f.WriteData(0x4D) // WRITE ID (MFM)
	f.WriteData(0x00)
	f.WriteData(0x02) // N=2 (512)
	f.WriteData(0x04) // SC=4
	f.WriteData(0x54) // GPL
	f.WriteData(filler)
	for j := 0; j < 4; j++ {
		f.WriteData(12)          // C
		f.WriteData(0)           // H
		f.WriteData(byte(j + 1)) // R
		f.WriteData(2)           // N
	}
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 != 0 {
		t.Fatalf("FORMAT abnormal: ST0=%02X", res[0])
	}

	// READ DATA each sector and verify the data field is all filler.
	for r := byte(1); r <= 4; r++ {
		f.headPos[0] = 0 // rewind so the read can find the new sectors
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(12)
		f.WriteData(0)
		f.WriteData(r)
		f.WriteData(2)
		f.WriteData(r)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		got := make([]byte, 0, 512)
		for i := 0; i < 512; i++ {
			got = append(got, f.ReadData())
		}
		res := drainResult(t, f, 7)
		if res[0]&st0IC0 != 0 {
			t.Errorf("READ DATA r=%d abnormal: ST0=%02X", r, res[0])
		}
		for i, b := range got {
			if b != filler {
				t.Errorf("r=%d byte %d: got %02X, want %02X", r, i, b, filler)
				break
			}
		}
	}
}

func TestUPD765FormatTrackOverflow(t *testing.T) {
	// FORMAT TRACK with too many sectors should fail gracefully (ST1.ND
	// + abnormal termination) instead of silently producing a corrupt
	// track.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x4D)
	f.WriteData(0x00) // HD=0 US=0
	f.WriteData(0x02) // N=2 (512 bytes)
	f.WriteData(0x20) // SC=32 — far too many for a DD track
	f.WriteData(0x54)
	f.WriteData(0xE5)

	// Send 32*4 = 128 CHRN bytes.
	for j := 0; j < 32; j++ {
		f.WriteData(0)
		f.WriteData(0)
		f.WriteData(byte(j + 1))
		f.WriteData(2)
	}
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("overflow FORMAT ST0=%02X: IC0 clear, want abnormal", res[0])
	}
	if res[1]&st1ND == 0 {
		t.Errorf("overflow FORMAT ST1=%02X: ND clear, want set", res[1])
	}
}

func TestUPD765FormatTrackRejectBadN(t *testing.T) {
	// N > 8 is nonsensical (sector ≥ 32 KB); FDC should reject.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x4D)
	f.WriteData(0x00)
	f.WriteData(0x0A) // N=10 — invalid
	f.WriteData(0x01)
	f.WriteData(0x54)
	f.WriteData(0xE5)
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("bad-N FORMAT ST0=%02X: IC0 clear, want abnormal", res[0])
	}
}

// writeSector is a helper for SCAN tests: writes a known byte pattern
// into sector R=1 cyl=0 of drive 0.
func writeSectorForScan(t *testing.T, f *UPD765, data []byte) {
	t.Helper()
	f.WriteData(0x45) // WRITE DATA (MFM)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for _, b := range data {
		f.WriteData(b)
	}
	drainResult(t, f, 7)
}

func issueScanAndCompare(t *testing.T, f *UPD765, cmd byte, compare []byte) []byte {
	t.Helper()
	f.WriteData(cmd)
	f.WriteData(0x00) // HDS|US
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT
	f.WriteData(0x4E) // GPL
	f.WriteData(0x01) // STP (unused in single-sector model)
	for _, b := range compare {
		f.WriteData(b)
	}
	return drainResult(t, f, 7)
}

func TestUPD765ScanEqualHit(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}
	writeSectorForScan(t, f, data)

	// SCAN EQUAL 0x51 (0x11 | 0x40 MFM) with matching data.
	res := issueScanAndCompare(t, f, 0x51, data)
	if res[2]&0x08 == 0 {
		t.Errorf("SCAN EQUAL hit: ST2=%02X, SH clear, want set", res[2])
	}
	if res[2]&0x04 != 0 {
		t.Errorf("SCAN EQUAL hit: ST2=%02X, SN set, want clear", res[2])
	}
}

func TestUPD765ScanEqualMiss(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}
	writeSectorForScan(t, f, data)

	// Change one byte of the comparison data so the scan fails.
	compare := append([]byte(nil), data...)
	compare[100] = 0xAA
	res := issueScanAndCompare(t, f, 0x51, compare)
	if res[2]&0x08 != 0 {
		t.Errorf("SCAN EQUAL miss: ST2=%02X, SH set, want clear", res[2])
	}
	if res[2]&0x04 == 0 {
		t.Errorf("SCAN EQUAL miss: ST2=%02X, SN clear, want set", res[2])
	}
}

func TestUPD765ScanEqualDontCare(t *testing.T) {
	// 0xFF from the CPU is "don't care" — bytes at those positions
	// should not affect the scan outcome.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i & 0xFF)
	}
	writeSectorForScan(t, f, data)

	compare := make([]byte, 512)
	for i := range compare {
		compare[i] = 0xFF // all don't-cares
	}
	res := issueScanAndCompare(t, f, 0x51, compare)
	if res[2]&0x08 == 0 {
		t.Errorf("SCAN EQUAL all-don't-care: ST2=%02X, SH clear, want set", res[2])
	}
}

func TestUPD765ScanLowOrEqual(t *testing.T) {
	// SCAN LO/EQ succeeds if every CPU byte is <= the disk byte.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	data := make([]byte, 512)
	for i := range data {
		data[i] = 0x80
	}
	writeSectorForScan(t, f, data)

	// All 0x20 ≤ 0x80 → hit.
	compare := make([]byte, 512)
	for i := range compare {
		compare[i] = 0x20
	}
	res := issueScanAndCompare(t, f, 0x59, compare)
	if res[2]&0x08 == 0 {
		t.Errorf("SCAN LO/EQ (all lower): ST2=%02X, SH clear, want set", res[2])
	}

	// One byte > 0x80 → miss.
	compare[50] = 0xFE
	res = issueScanAndCompare(t, f, 0x59, compare)
	if res[2]&0x08 != 0 {
		t.Errorf("SCAN LO/EQ (one higher): ST2=%02X, SH set, want clear", res[2])
	}
	if res[2]&0x04 == 0 {
		t.Errorf("SCAN LO/EQ (one higher): ST2=%02X, SN clear, want set", res[2])
	}
}

func TestUPD765ScanHighOrEqual(t *testing.T) {
	// SCAN HI/EQ succeeds if every CPU byte is >= the disk byte.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	data := make([]byte, 512)
	for i := range data {
		data[i] = 0x40
	}
	writeSectorForScan(t, f, data)

	compare := make([]byte, 512)
	for i := range compare {
		compare[i] = 0x80 // all higher → hit
	}
	res := issueScanAndCompare(t, f, 0x5D, compare)
	if res[2]&0x08 == 0 {
		t.Errorf("SCAN HI/EQ (all higher): ST2=%02X, SH clear, want set", res[2])
	}

	// One byte < 0x40 → miss.
	compare[200] = 0x10
	res = issueScanAndCompare(t, f, 0x5D, compare)
	if res[2]&0x08 != 0 {
		t.Errorf("SCAN HI/EQ (one lower): ST2=%02X, SH set, want clear", res[2])
	}
}

func TestUPD765ReadDataMultiSector(t *testing.T) {
	// READ DATA with R=1, EOT=3 should transfer three full sectors in
	// a row and finish with ST1.EN set.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46) // READ DATA (MFM)
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02) // N = 2 (512 bytes)
	f.WriteData(0x03) // EOT = 3
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	// Three sectors of 512 bytes each.
	for i := 0; i < 3*512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[1]&st1EN == 0 {
		t.Errorf("multi-sector EOT: ST1=%02X, EN clear, want set", res[1])
	}
	if res[0]&st0IC0 != 0 {
		t.Errorf("multi-sector: ST0=%02X, IC0 set, want normal", res[0])
	}
}

func TestUPD765ReadDeletedDataProper(t *testing.T) {
	// READ DATA on a deleted-DAM sector with SK=0 should read the data
	// and set ST2.CM. READ DELETED DATA on the same sector should read
	// the data WITHOUT CM (because the DAM matches the request).
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), true, false) // deleted DAM
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)

	// READ DATA (0x06) — wants FB, finds F8 → should set CM.
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x40 == 0 {
		t.Errorf("READ DATA on deleted sector: ST2=%02X, CM clear, want set", res[2])
	}

	// Reset head pos and re-read with READ DELETED DATA (0x0C) — wants
	// F8, finds F8 → CM should be clear.
	f.headPos[0] = 0
	f.WriteData(0x4C) // READ DELETED DATA (MFM)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res = drainResult(t, f, 7)
	if res[2]&0x40 != 0 {
		t.Errorf("READ DELETED DATA on deleted sector: ST2=%02X, CM set, want clear", res[2])
	}
}

func TestUPD765ReadDataSKBitSkipsDeleted(t *testing.T) {
	// READ DATA with SK=1 on a deleted-DAM sector should SKIP it,
	// resulting in "sector not found" (ST1.ND) rather than reading the
	// data with ST2.CM.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), true, false) // deleted
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)

	// READ DATA with SK=1: opcode 0x26 (0x06 | 0x20 SK) + 0x40 MFM = 0x66.
	f.WriteData(0x66)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	res := drainResult(t, f, 7)
	if res[1]&st1ND == 0 {
		t.Errorf("READ DATA SK=1 over deleted sector: ST1=%02X, ND clear, want set", res[1])
	}
}

func TestUPD765ReadDiag(t *testing.T) {
	// READ DIAGNOSTIC returns the raw track bytes from the index. For
	// a freshly-loaded track we expect at least the pre-index GAP I
	// filler (0x4E for MFM) at position 0.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x42) // READ DIAGNOSTIC (MFM)
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT = 1 sector worth of bytes
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	// Read 512 bytes (N=2, EOT=1) from the raw track stream.
	bytes := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		bytes = append(bytes, f.ReadData())
	}
	drainResult(t, f, 7)

	// The first byte should be the pre-index gap byte (0x4E MFM).
	if bytes[0] != 0x4E {
		t.Errorf("READ DIAG first byte = %02X, want 0x4E (MFM GAP I)", bytes[0])
	}
}

func TestUPD765SpeedlockRetryCounter(t *testing.T) {
	// Speedlock detection is narrow on purpose (see trackSpeedlockRetry
	// comment): only the exact pattern C=0 H=0 R=2 EOT=2 matches. Any
	// other READ DATA resets the counter so legitimate BDOS retries
	// don't get mangled.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetSpeedlockEnabled(true)

	// Read sector 2 (the Speedlock pattern).
	readSpeedlock := func() {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00) // C
		f.WriteData(0x00) // H
		f.WriteData(0x02) // R = 2
		f.WriteData(0x02)
		f.WriteData(0x02) // EOT = 2 (single-sector)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		for i := 0; i < 512; i++ {
			f.ReadData()
		}
		drainResult(t, f, 7)
		f.headPos[0] = 0
	}
	// Read sector 1 (NOT the Speedlock pattern — resets the counter).
	readOther := func() {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x01) // R = 1
		f.WriteData(0x02)
		f.WriteData(0x01)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		for i := 0; i < 512; i++ {
			f.ReadData()
		}
		drainResult(t, f, 7)
		f.headPos[0] = 0
	}

	readSpeedlock()
	if f.speedlockCounter != 0 {
		t.Errorf("after first Speedlock read: counter=%d, want 0", f.speedlockCounter)
	}
	readSpeedlock()
	if f.speedlockCounter == 0 {
		t.Errorf("after second Speedlock read: counter=0, want >0")
	}
	readOther() // not the Speedlock pattern → reset
	if f.speedlockCounter != 0 {
		t.Errorf("after off-pattern read: counter=%d, want 0", f.speedlockCounter)
	}
}

func TestUPD765SpeedlockDisabled(t *testing.T) {
	// With Speedlock disabled (default), the counter never moves.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	for i := 0; i < 3; i++ {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x01)
		f.WriteData(0x02)
		f.WriteData(0x01)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		for j := 0; j < 512; j++ {
			f.ReadData()
		}
		drainResult(t, f, 7)
		f.headPos[0] = 0
	}
	if f.speedlockCounter != 0 {
		t.Errorf("disabled: counter=%d, want 0", f.speedlockCounter)
	}
}

func TestUPD765SpeedlockManglesOnRetry(t *testing.T) {
	// With Speedlock enabled, a second read of the same sector should
	// return at least one byte different from the first read — that's
	// the whole point of the hack. The mangling pattern is
	// deterministic based on the byte offset, but the first read
	// should always be clean.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetSpeedlockEnabled(true)

	// Speedlock only triggers on C=0 H=0 R=2 EOT=2 (see
	// trackSpeedlockRetry comment). Use the exact pattern.
	read := func() []byte {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x02)
		f.WriteData(0x02)
		f.WriteData(0x02)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		got := make([]byte, 0, 512)
		for i := 0; i < 512; i++ {
			got = append(got, f.ReadData())
		}
		drainResult(t, f, 7)
		f.headPos[0] = 0
		return got
	}

	first := read()
	second := read()

	diffs := 0
	for i := range first {
		if first[i] != second[i] {
			diffs++
		}
	}
	if diffs == 0 {
		t.Error("Speedlock-enabled retry returned identical bytes; expected mangling")
	}
}

func TestUPD765SpeedlockRetryReportsCRCError(t *testing.T) {
	// Speedlock's protection relies on the retry read producing
	// different data AND the CRC check failing (mirroring a real
	// weak sector). Verify both happen together: the second read of
	// the Speedlock pattern sector must terminate with ST2.DD set.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetSpeedlockEnabled(true)

	read := func() []byte {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x02) // R = 2 (Speedlock pattern)
		f.WriteData(0x02)
		f.WriteData(0x02) // EOT = 2
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		for i := 0; i < 512; i++ {
			f.ReadData()
		}
		res := drainResult(t, f, 7)
		f.headPos[0] = 0
		return res
	}

	first := read()
	if first[2]&0x20 != 0 {
		t.Errorf("first read should be clean but ST2=%02X (DD set)", first[2])
	}
	second := read()
	if second[2]&0x20 == 0 {
		t.Errorf("second read should report ST2.DD (CRC error); ST2=%02X", second[2])
	}
	if second[0]&st0IC0 == 0 {
		t.Errorf("second read should be abnormal; ST0=%02X", second[0])
	}
}

func TestUPD765SpeedlockDisabledReadsAreClean(t *testing.T) {
	// With Speedlock disabled, two successive reads of the same sector
	// should always return identical bytes.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	read := func() []byte {
		f.WriteData(0x46)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x00)
		f.WriteData(0x01)
		f.WriteData(0x02)
		f.WriteData(0x01)
		f.WriteData(0x4E)
		f.WriteData(0xFF)
		got := make([]byte, 0, 512)
		for i := 0; i < 512; i++ {
			got = append(got, f.ReadData())
		}
		drainResult(t, f, 7)
		f.headPos[0] = 0
		return got
	}
	for i, b := range read() {
		if b != read()[i] {
			t.Errorf("Speedlock disabled: reads differ at byte %d", i)
			break
		}
	}
}

func TestUPD765SpeedlockFirstReadOnZeroCHRNSector(t *testing.T) {
	// A sector with C=H=R=N=0 packs to id=0. The initial
	// lastSectorID sentinel must be a distinct value so the very
	// first read doesn't incorrectly bump the counter.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 0)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 0, 0, false) // CHRN all zero
	b.dataAdd(make([]byte, 128), false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)
	f.SetSpeedlockEnabled(true)

	f.WriteData(0x46)
	f.WriteData(0x00) // HDS|US
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x00) // R = 0
	f.WriteData(0x00) // N = 0 (128 bytes)
	f.WriteData(0x00) // EOT = 0 (single sector)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 128; i++ {
		f.ReadData()
	}
	drainResult(t, f, 7)
	if f.speedlockCounter != 0 {
		t.Errorf("first read of zero-CHRN sector: counter=%d, want 0", f.speedlockCounter)
	}
}

func TestUPD765SpeedlockResetClearsCounter(t *testing.T) {
	// Reset should wipe the retry counter so a subsequent same-sector
	// read starts fresh from 0.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.SetSpeedlockEnabled(true)

	// Bump the counter.
	f.speedlockCounter = 5
	f.lastSectorID = 0xDEADBEEF

	f.Reset()
	if f.speedlockCounter != 0 {
		t.Errorf("after Reset: counter=%d, want 0", f.speedlockCounter)
	}
	if f.lastSectorID != 0 {
		t.Errorf("after Reset: lastSectorID=%08X, want 0", f.lastSectorID)
	}
}

func TestUPD765EjectMidRead(t *testing.T) {
	// Issue READ DATA, consume some bytes, then eject the disk. The
	// next ReadData should return cleanly without crashing — the FDC
	// state machine resets on AttachDisk(_, nil).
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	// Pull a few bytes — we're now in execution phase.
	for i := 0; i < 10; i++ {
		_ = f.ReadData()
	}
	// Eject the disk.
	f.AttachDisk(0, nil)
	// Subsequent ReadData / ReadStatus must not panic; FDC should be
	// idle.
	if st := f.ReadStatus(); st != msrRQM {
		t.Errorf("after mid-read eject: status=%02X, want %02X (idle)", st, msrRQM)
	}
	_ = f.ReadData() // should be safe (no crash)
}

func TestUPD765MultiDriveDistinctDisks(t *testing.T) {
	// Mount two distinct disks in drives A and B; SENSE DRIVE for each
	// should report ready, and READ DATA from each should return the
	// expected sector data.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))
	f.AttachDisk(1, loadTestDisk(t))

	for _, us := range []byte{0, 1} {
		f.WriteData(0x04)
		f.WriteData(us)
		res := drainResult(t, f, 1)
		if res[0]&st3RY == 0 {
			t.Errorf("drive %d: ST3=%02X: RY clear, want set", us, res[0])
		}
	}

	// Eject drive A — drive B should still be ready.
	f.AttachDisk(0, nil)
	f.WriteData(0x04)
	f.WriteData(0x00)
	res := drainResult(t, f, 1)
	if res[0]&st3RY != 0 {
		t.Errorf("drive 0 after eject: ST3=%02X: RY set, want clear", res[0])
	}
	f.WriteData(0x04)
	f.WriteData(0x01)
	res = drainResult(t, f, 1)
	if res[0]&st3RY == 0 {
		t.Errorf("drive 1 after eject of drive 0: ST3=%02X: RY clear, want set", res[0])
	}
}

func TestUPD765SenseDriveDriveOne(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SENSE DRIVE STATUS for drive 1 — should report not ready.
	f.WriteData(0x04)
	f.WriteData(0x01) // HD=0 US=1
	res := drainResult(t, f, 1)
	st3 := res[0]
	if st3&st3RY != 0 {
		t.Errorf("ST3=%02X: RY set, want clear (drive 1 has no disk)", st3)
	}
}

func TestUPD765ReadID(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SEEK to cylinder 5 first.
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(5)
	// Drain the seek-complete latch so READ ID starts clean.
	f.WriteData(0x08)
	drainResult(t, f, 2)

	// READ ID drive 0 head 0
	f.WriteData(0x4A) // 0x0A | 0x40 (MFM)
	f.WriteData(0x00) // HD=0 US=0
	res := drainResult(t, f, 7)

	// Expect ST0 normal (no IC bits), CHRN = (5,0,1,2)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("READ ID ST0=%02X: IC bits set, expected normal", res[0])
	}
	if res[3] != 5 || res[4] != 0 || res[5] != 1 || res[6] != 2 {
		t.Errorf("READ ID CHRN = (%d,%d,%d,%d), want (5,0,1,2)", res[3], res[4], res[5], res[6])
	}
}

func TestUPD765ReadDataSector(t *testing.T) {
	f := NewUPD765()
	disk := loadTestDisk(t)
	f.AttachDisk(0, disk)

	// SEEK drive 0 to cyl 3
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(3)
	f.WriteData(0x08) // SENSE INT to drain seek
	drainResult(t, f, 2)

	// READ DATA cmd: 0x46 (MFM bit + READ DATA), then 8 params
	//   HDS|US, C, H, R, N, EOT, GPL, DTL
	f.WriteData(0x46)
	f.WriteData(0x00) // HD=0 US=0
	f.WriteData(0x03) // C
	f.WriteData(0x00) // H
	f.WriteData(0x04) // R = 4
	f.WriteData(0x02) // N = 2 (512 bytes)
	f.WriteData(0x04) // EOT (we read just one)
	f.WriteData(0x4E) // GPL
	f.WriteData(0xFF) // DTL

	// Now in execution phase — read 512 bytes.
	gotData := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		st := f.ReadStatus()
		if st&msrRQM == 0 {
			t.Fatalf("byte %d: RQM clear (status=%02X)", i, st)
		}
		if st&msrEXM == 0 {
			t.Fatalf("byte %d: EXM clear during execution (status=%02X)", i, st)
		}
		gotData = append(gotData, f.ReadData())
	}

	// Verify the first three bytes encode (cylinder, head, sector ID).
	if gotData[0] != 3 || gotData[1] != 0 || gotData[2] != 4 {
		t.Errorf("sector data prefix = (%d,%d,%d), want (3,0,4)",
			gotData[0], gotData[1], gotData[2])
	}
	for i := 3; i < 512; i++ {
		if gotData[i] != 0xCC {
			t.Errorf("byte %d = %02X, want 0xCC", i, gotData[i])
			break
		}
	}

	// FDC should now be in result phase.
	res := drainResult(t, f, 7)
	if res[0]&(st0IC0|st0IC1) != 0 {
		t.Errorf("READ DATA ST0=%02X: IC bits set, expected normal", res[0])
	}
}

func TestUPD765ReadDataSectorNotFound(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// READ DATA for sector R=99 (does not exist).
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00) // C=0
	f.WriteData(0x00)
	f.WriteData(0x63) // R=99
	f.WriteData(0x02)
	f.WriteData(0x63)
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	// Expect to go straight to result phase with abnormal termination.
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("ST0=%02X: IC0 clear, expected abnormal", res[0])
	}
	if res[1]&st1ND == 0 {
		t.Errorf("ST1=%02X: ND clear, expected no-data", res[1])
	}
}

func TestUPD765InvalidCommand(t *testing.T) {
	f := NewUPD765()
	// 0xE0 doesn't match any command.
	f.WriteData(0xE0)
	res := drainResult(t, f, 1)
	if res[0] != 0x80 {
		t.Errorf("INVALID ST0=%02X, want 0x80 (IC=10)", res[0])
	}
}

func TestUPD765SenseIntNoSeekPending(t *testing.T) {
	f := NewUPD765()
	// No prior SEEK / RECALIBRATE — SENSE INT collapses to the INVALID
	// command path: a single ST0=0x80 byte, no PCN.
	f.WriteData(0x08)
	res := drainResult(t, f, 1)
	if res[0] != 0x80 {
		t.Errorf("SENSE INT no-seek ST0=%02X, want 0x80", res[0])
	}
}

func TestUPD765MultiDriveSeek(t *testing.T) {
	// Two seeks on different drives in succession; SENSE INT should
	// report the right ST0 + PCN for each. This caught a bug where
	// seekResult was a single slot and the first SENSE INT returned the
	// second seek's ST0 bits.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	// SEEK drive 0 to cyl 7
	f.WriteData(0x0F)
	f.WriteData(0x00)
	f.WriteData(7)
	// SEEK drive 1 to cyl 19
	f.WriteData(0x0F)
	f.WriteData(0x01)
	f.WriteData(19)

	// First SENSE INT — picks drive 0 (lowest index with seekDone set).
	f.WriteData(0x08)
	res := drainResult(t, f, 2)
	if res[0]&st0US1 != 0 {
		t.Errorf("first SENSE INT ST0=%02X: US1 set, expected drive 0", res[0])
	}
	if res[1] != 7 {
		t.Errorf("first SENSE INT PCN=%d, want 7", res[1])
	}

	// Second SENSE INT — picks drive 1.
	f.WriteData(0x08)
	res = drainResult(t, f, 2)
	if res[0]&st0US0 == 0 {
		t.Errorf("second SENSE INT ST0=%02X: US0 clear, expected drive 1", res[0])
	}
	if res[1] != 19 {
		t.Errorf("second SENSE INT PCN=%d, want 19", res[1])
	}
}

func TestUPD765ReadDataCRCError(t *testing.T) {
	// Build a disk with a CRC-error sector and confirm that READ DATA
	// reports ST2.DD (data error in data field) in the result phase.
	d := &Disk{
		Sides:     1,
		Cylinders: 1,
		Tracks:    [][]*Track{{nil}},
	}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	// dataAdd with crcError=true: the stored CRC bytes will not match
	// the running CRC the FDC computes when reading the data back.
	b.dataAdd(make([]byte, 512), false, true)
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)
	f.WriteData(0x46) // READ DATA (MFM)
	f.WriteData(0x00) // HD=0 US=0
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02) // N = 2
	f.WriteData(0x01) // EOT
	f.WriteData(0x4E) // GPL
	f.WriteData(0xFF) // DTL

	// Drain all 512 data bytes.
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	// Now in result phase — verify ST2 has the DD bit set.
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 == 0 {
		t.Errorf("CRC-error read ST0=%02X: IC0 clear, want abnormal", res[0])
	}
	if res[2]&0x20 == 0 {
		t.Errorf("CRC-error read ST2=%02X: DD bit clear, want set", res[2])
	}
}

func TestUPD765ReadDataDeletedDAM(t *testing.T) {
	// A sector with a deleted data mark should set ST2.CM (control mark)
	// in the result phase.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), true, false)
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
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x40 == 0 {
		t.Errorf("deleted-DAM read ST2=%02X: CM bit clear, want set", res[2])
	}
}

func TestUPD765ReadDataWrongCylinder(t *testing.T) {
	// Build a track whose IDAM C field doesn't match the seek position.
	// READ DATA should still locate the sector by R but flag ST2.WC
	// (wrong cylinder) on the result.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	// IDAM claims C=99 but the disk geometry puts it at C=0.
	b.idAdd(99, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)
	// READ DATA C=0 (the seek position) — IDAM C=99 is the mismatch.
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x10 == 0 {
		t.Errorf("wrong-cylinder read ST2=%02X: WC bit clear, want set", res[2])
	}
	if res[0]&st0IC0 == 0 {
		t.Errorf("wrong-cylinder ST0=%02X: IC0 clear, want abnormal", res[0])
	}
}

func TestUPD765ReadDataSkipsBadIDCRC(t *testing.T) {
	// Build a track with two sectors: sector 1 has a corrupted ID CRC,
	// sector 2 is clean. READ DATA R=2 should find and read sector 2
	// without ever erroring on sector 1's bad CRC.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, true) // sector 1: ID CRC error
	b.dataAdd(make([]byte, 512), false, false)
	b.idAdd(0, 0, 2, 2, false) // sector 2: clean
	want := make([]byte, 512)
	for i := range want {
		want[i] = byte(0xA0 + i&0x0F)
	}
	b.dataAdd(want, false, false)
	b.gap4Add()
	d.Tracks[0][0] = tr

	f := NewUPD765()
	f.AttachDisk(0, d)
	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x02) // R = 2
	f.WriteData(0x02)
	f.WriteData(0x02)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	got := make([]byte, 0, 512)
	for i := 0; i < 512; i++ {
		got = append(got, f.ReadData())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte %d: got %02X, want %02X", i, got[i], want[i])
			break
		}
	}
	res := drainResult(t, f, 7)
	if res[0]&st0IC0 != 0 {
		t.Errorf("expected normal termination, got ST0=%02X", res[0])
	}
}

func TestUPD765ReadDataDeletedDAMCRCStillValid(t *testing.T) {
	// A deleted-DAM sector with a valid CRC should NOT report ST2.DD —
	// only ST2.CM. The CRC primer must use F8 (deleted) not FB so the
	// computed CRC matches the on-track CRC.
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	b.dataAdd(make([]byte, 512), true, false) // deleted, CRC OK
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
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)
	if res[2]&0x40 == 0 {
		t.Errorf("ST2.CM clear (was %02X), want set for deleted DAM", res[2])
	}
	if res[2]&0x20 != 0 {
		t.Errorf("ST2.DD set (was %02X) — CRC primer must use F8 for deleted DAM", res[2])
	}
}

func TestUPD765ReadIDAdvancesHeadPos(t *testing.T) {
	// Successive READ ID commands on the same track should walk through
	// every sector in physical order, not return the same first IDAM
	// every time. This is enabled by headPos tracking.
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	seen := make([]byte, 0, 9)
	for i := 0; i < 9; i++ {
		f.WriteData(0x4A) // READ ID
		f.WriteData(0x00) // HD=0 US=0
		res := drainResult(t, f, 7)
		seen = append(seen, res[5]) // R byte
	}
	// We expect to see all 9 sector IDs (1..9) in some physical order.
	have := make(map[byte]bool)
	for _, r := range seen {
		have[r] = true
	}
	for r := byte(1); r <= 9; r++ {
		if !have[r] {
			t.Errorf("READ ID never returned R=%d (saw %v)", r, seen)
		}
	}
}

func TestUPD765NoDiskNotReady(t *testing.T) {
	f := NewUPD765()
	// READ DATA without a disk attached — should fail with NR.
	f.WriteData(0x46)
	f.WriteData(0x00) // HD=0 US=0
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01)
	f.WriteData(0x02)
	f.WriteData(0x01)
	f.WriteData(0x4E)
	f.WriteData(0xFF)

	res := drainResult(t, f, 7)
	if res[0]&st0NR == 0 {
		t.Errorf("ST0=%02X: NR clear, expected drive-not-ready", res[0])
	}
}
