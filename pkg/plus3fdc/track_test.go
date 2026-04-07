package plus3fdc

import (
	"testing"
)

func TestTrackBuilderEmitsValidIDAM(t *testing.T) {
	tr := newTrack(bytesPerTrackDD, 0xE5, 5, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	if !b.preindexAdd() {
		t.Fatal("preindexAdd failed")
	}
	if !b.postindexAdd() {
		t.Fatal("postindexAdd failed")
	}
	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i)
	}
	if !b.idAdd(5, 0, 1, 2, false) {
		t.Fatal("idAdd failed")
	}
	if _, ok := b.dataAdd(data, false, false); !ok {
		t.Fatal("dataAdd failed")
	}
	if !b.gap4Add() {
		t.Fatal("gap4Add failed")
	}

	// Walking the track stream should find a single IDAM with C=5,H=0,R=1,N=2.
	c, h, r, n, idEnd, ok := tr.idAt(0)
	if !ok {
		t.Fatal("idAt: expected to find an IDAM")
	}
	if c != 5 || h != 0 || r != 1 || n != 2 {
		t.Errorf("IDAM CHRN = (%d,%d,%d,%d), want (5,0,1,2)", c, h, r, n)
	}

	// And the data field should be locatable from idEnd onwards.
	dataStart, deleted, ok := tr.dataAt(idEnd)
	if !ok {
		t.Fatal("dataAt: expected to find a DAM")
	}
	if deleted {
		t.Errorf("dataAt: deleted=true, expected false")
	}
	for i := 0; i < 512; i++ {
		if got := tr.readByte(dataStart + i); got != byte(i) {
			t.Errorf("data byte %d: got %02X, want %02X", i, got, byte(i))
			break
		}
	}
}

func TestTrackBuilderDeletedDataMark(t *testing.T) {
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	if !b.preindexAdd() || !b.postindexAdd() {
		t.Fatal("track header failed")
	}
	if !b.idAdd(0, 0, 1, 2, false) {
		t.Fatal("idAdd failed")
	}
	if _, ok := b.dataAdd(make([]byte, 512), true, false); !ok {
		t.Fatal("dataAdd failed")
	}
	b.gap4Add()

	_, _, _, _, idEnd, ok := tr.idAt(0)
	if !ok {
		t.Fatal("idAt failed")
	}
	_, deleted, ok := tr.dataAt(idEnd)
	if !ok {
		t.Fatal("dataAt failed")
	}
	if !deleted {
		t.Error("expected deleted=true for deleted data mark")
	}
}

func TestTrackWeakSectorReturnsVariableData(t *testing.T) {
	// Build a track with a weak data range and verify that reads from
	// the weak region produce varied bytes. This is the foundation for
	// copy-protection support.
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()
	b.idAdd(0, 0, 1, 2, false)
	dataStart, ok := b.dataAdd(make([]byte, 512), false, false)
	if !ok {
		t.Fatal("dataAdd failed")
	}
	b.gap4Add()
	b.markWeakRange(dataStart, 16)

	// Sample 32 byte values from the weak region and assert that we
	// observed at least three distinct values — random.Read on 32 bytes
	// is overwhelmingly unlikely to return the same byte every time.
	seen := make(map[byte]bool)
	for i := 0; i < 32; i++ {
		seen[tr.readByte(dataStart+i%16)] = true
	}
	if len(seen) < 3 {
		t.Errorf("weak sector reads only produced %d distinct values; expected randomness", len(seen))
	}
}

func TestTrackWalkMultipleIDAMs(t *testing.T) {
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
	b := newTrackBuilder(tr, gapMFM)
	b.preindexAdd()
	b.postindexAdd()

	// Three sectors in physical order: R = 5, 1, 3 (interleaved).
	for _, r := range []byte{5, 1, 3} {
		if !b.idAdd(0, 0, r, 2, false) {
			t.Fatalf("idAdd r=%d failed", r)
		}
		if _, ok := b.dataAdd(make([]byte, 512), false, false); !ok {
			t.Fatalf("dataAdd r=%d failed", r)
		}
	}
	b.gap4Add()

	// Walk all IDAMs and confirm we see them in physical (not numerical) order.
	got := []byte{}
	pos := 0
	for {
		_, _, r, _, idEnd, ok := tr.idAt(pos)
		if !ok {
			break
		}
		got = append(got, r)
		// Skip past the data field too so the next iteration starts after it.
		ds, _, dok := tr.dataAt(idEnd)
		if !dok {
			break
		}
		pos = ds + 512
	}
	want := []byte{5, 1, 3}
	if len(got) != len(want) {
		t.Fatalf("found %d IDAMs (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IDAM %d: got R=%d, want R=%d", i, got[i], want[i])
		}
	}
}

func TestSectorLengthClampsN(t *testing.T) {
	// Valid N values must pass through untouched; anything above 8 is
	// clamped to 8 (the µPD765 hardware caps sector sizes at 128 << 8
	// = 32 KB). The critical case is that the function never produces
	// a negative or zero length via integer overflow, which a raw
	// `128 << n` with n=56 would do.
	cases := []struct {
		in   byte
		want int
	}{
		{0, 128},
		{1, 256},
		{2, 512},
		{3, 1024},
		{8, 32768},
		{9, 32768},   // clamp
		{56, 32768},  // would overflow to negative without clamp
		{57, 32768},  // would overflow to zero without clamp
		{255, 32768}, // would overflow to zero without clamp
	}
	for _, c := range cases {
		if got := sectorLength(c.in); got != c.want {
			t.Errorf("sectorLength(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseDSKRejectsMaliciousN(t *testing.T) {
	// Build a minimal DSK where the sector info table claims N=0xFF
	// (impossible — sector size would overflow 32 bits). Pre-fix this
	// would panic with "slice bounds out of range"; post-fix it
	// either succeeds with a clamped length or returns a clean error.
	hdr := make([]byte, dskHeaderSize)
	copy(hdr, dskSignatureStd)
	hdr[0x30] = 1 // 1 cylinder
	hdr[0x31] = 1 // 1 side
	// Track size: 256 header + 32KB data = enough for the clamped
	// interpretation, rounded to a 256-byte boundary.
	trackTotal := trackHeaderSize + 32768
	trackTotal = (trackTotal + 255) &^ 255
	hdr[0x32] = byte(trackTotal & 0xFF)
	hdr[0x33] = byte(trackTotal >> 8)

	track := make([]byte, trackTotal)
	copy(track, trackInfoSignature)
	track[0x14] = 0xFF // malicious N
	track[0x15] = 1    // one sector
	track[0x17] = 0xE5
	// Sector info: C=0 H=0 R=1 N=0xFF
	track[sectorInfoOffset+0] = 0
	track[sectorInfoOffset+1] = 0
	track[sectorInfoOffset+2] = 1
	track[sectorInfoOffset+3] = 0xFF

	data := append(hdr, track...)
	// The test is that this doesn't panic — an error return is fine.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseDSK panicked on malicious N=0xFF: %v", r)
		}
	}()
	_, _ = ParseDSK(data)
}

func TestCRC16Standard(t *testing.T) {
	// Standard CRC-16-CCITT test vector: "123456789" with init 0xFFFF.
	// Expected: 0x29B1.
	crc := uint16(0xFFFF)
	for _, b := range []byte("123456789") {
		crc = crcUpdate(crc, b)
	}
	if crc != 0x29B1 {
		t.Errorf("CRC-16-CCITT(123456789) = 0x%04X, want 0x29B1", crc)
	}
}

func TestTrackBuilderCRCErrorInjection(t *testing.T) {
	// idAdd / dataAdd with crcError=true should produce a track whose
	// CRC bytes don't match the data — by flipping the LSB of the
	// computed CRC. Here we just confirm the byte pattern differs from
	// the same call without crcError.
	mkTrack := func(crcErr bool) []byte {
		tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, 2)
		b := newTrackBuilder(tr, gapMFM)
		b.preindexAdd()
		b.postindexAdd()
		b.idAdd(0, 0, 1, 2, crcErr)
		b.dataAdd(make([]byte, 512), false, crcErr)
		b.gap4Add()
		return tr.data
	}
	good := mkTrack(false)
	bad := mkTrack(true)
	if len(good) != len(bad) {
		t.Fatalf("track length differs: good=%d bad=%d", len(good), len(bad))
	}
	diffs := 0
	for i := range good {
		if good[i] != bad[i] {
			diffs++
		}
	}
	// We expect exactly two single-bit flips (one in the ID CRC, one in
	// the data CRC), each producing one differing byte.
	if diffs != 2 {
		t.Errorf("got %d differing bytes between clean and CRC-error tracks; want 2", diffs)
	}
}

func TestParseDSKEDSKWeakSector(t *testing.T) {
	// Build a small EDSK by hand with a single sector that stores
	// dataBytes > 128<<N — the convention used by EDSK to encode a
	// weak sector with multiple data copies. The parsed Disk should
	// mark that sector's data range weak, so reads return varying
	// bytes.
	const (
		sides         = 1
		cylinders     = 1
		sectorsPerTrk = 1
		sectorN       = byte(2) // 512
		sectorIDLen   = 128 << sectorN
		actualLen     = sectorIDLen * 2 // two copies = weak sector
	)

	hdr := make([]byte, dskHeaderSize)
	copy(hdr, dskSignatureExt)
	hdr[0x30] = cylinders
	hdr[0x31] = sides
	trackTotal := trackHeaderSize + actualLen
	hdr[0x34] = byte(trackTotal / 256)

	track := make([]byte, trackTotal)
	copy(track, trackInfoSignature)
	track[0x10] = 0
	track[0x11] = 0
	track[0x14] = sectorN
	track[0x15] = sectorsPerTrk
	track[0x17] = 0xE5
	// Sector info entry — N=2 in the ID but actualLen bytes stored.
	track[sectorInfoOffset+0] = 0
	track[sectorInfoOffset+1] = 0
	track[sectorInfoOffset+2] = 1 // R=1
	track[sectorInfoOffset+3] = sectorN
	track[sectorInfoOffset+6] = byte(actualLen & 0xFF)
	track[sectorInfoOffset+7] = byte(actualLen >> 8)
	// Fill the data area with a recognisable pattern.
	for i := 0; i < actualLen; i++ {
		track[trackHeaderSize+i] = byte(i & 0xFF)
	}

	dsk := append(hdr, track...)
	d, err := ParseDSK(dsk)
	if err != nil {
		t.Fatalf("ParseDSK: %v", err)
	}
	tr := d.Track(0, 0)
	if tr == nil {
		t.Fatal("track 0,0 missing")
	}
	// The compatibility shim materialises a Sector by walking the
	// track. Reads from a weak region return random bytes, so two
	// successive FindSector calls should produce different data.
	a := tr.FindSector(1)
	b := tr.FindSector(1)
	if a == nil || b == nil {
		t.Fatal("FindSector returned nil")
	}
	same := 0
	for i := range a.Data {
		if a.Data[i] == b.Data[i] {
			same++
		}
	}
	// In a 512-byte fully-weak sector we expect ~1/256 bytes to match
	// by chance (~2). Anything close to 512 means the weak bitmap was
	// not set.
	if same > 100 {
		t.Errorf("two reads of a weak sector matched in %d/512 bytes; expected ~2", same)
	}
}
