package plus3fdc

// Track-level disk representation. Each track is stored as a stream of
// bytes representing what the FDC head reads as the disk spins past it,
// plus two parallel bitmaps:
//
//   clocks: 1 = the byte at this position has a missing-clock pattern
//           (used to mark address marks: A1 with clock = sync, FE with
//           clock = ID address mark, FB/F8 with clock = data mark)
//   weak:   1 = the byte is "weak" — reads return random data, used
//           by copy-protected disks
//
// The byte-stream representation is what enables the FDC to:
//   - find sectors with non-standard layouts
//   - support custom gap sizes / interleaves
//   - read weak sectors with copy protection
//   - implement READ DIAGNOSTIC (return the entire spinning-disk byte
//     stream including IDs and gaps)
//
// This is the canonical disk format. The Sector{} list view (disk.go)
// is a helper built on top of it, walking the track stream to find
// sector data for callers that just want CHRN + bytes.

import (
	"math/rand/v2"
)

// Default bytes-per-track for double-density (DD) MFM disks at 300RPM /
// 250kbps — the only density this package emits.
const bytesPerTrackDD = 6250

// Address mark and gap byte values.
const (
	gapByteMFM = 0x4E
	gapByteFM  = 0xFF // IBM3740 FM gap byte — referenced by the FM gapSpec row for future use
	syncByte   = 0x00
	a1Mark     = 0xA1 // MFM sync mark (with missing clock)
	idMark     = 0xFE // ID address mark
	dataMark   = 0xFB // Data address mark
	delDataMrk = 0xF8 // Deleted data address mark
	indexMark  = 0xFC // Index mark (FM only)
)

// gapKind selects which timing constants to use when laying out a track.
// Different disk formats use different gap layouts — IBM34 for
// CPC/+3, MGT for DISCiPLE/+D, TRDOS for Beta, and a FM variant for
// single-density disks.
type gapKind int

const (
	gapMFM   gapKind = iota // IBM34 MFM (CPC / +3 / DSK) — the default
	gapFM                   // IBM3740 FM (single-density)
	gapMGT                  // MGT MFM (DISCiPLE / +D / SAM Coupe)
	gapTRDOS                // TR-DOS MFM (Beta disk interface)
)

// gapSpec captures the byte values and lengths used to construct each
// region of a track. Indices into len[]:
//
//	0 - pre-index gap (before the index mark)
//	1 - post-index gap (after the index mark)
//	2 - GAP II (between ID field and data field)
//	3 - GAP III (between sectors)
type gapSpec struct {
	gap     byte // filler byte for gap regions
	sync    byte // sync byte
	syncLen int
	mark    int // 0xA1 for MFM, -1 for FM (no separate sync mark)
	len     [4]int
}

// gapSpecs maps gapKind to its spec — the byte counts for each gap
// follow the IBM System 34 (MFM) / IBM 3740 (FM) track format used by
// the CPC/+3, DISCiPLE/+D, and TR-DOS controllers.
var gapSpecs = [...]gapSpec{
	gapMFM:   {gap: gapByteMFM, sync: syncByte, syncLen: 12, mark: a1Mark, len: [4]int{80, 50, 22, 54}},
	gapFM:    {gap: gapByteFM, sync: syncByte, syncLen: 6, mark: -1, len: [4]int{40, 26, 11, 27}},
	gapMGT:   {gap: gapByteMFM, sync: syncByte, syncLen: 12, mark: a1Mark, len: [4]int{0, 60, 22, 24}},
	gapTRDOS: {gap: gapByteMFM, sync: syncByte, syncLen: 12, mark: a1Mark, len: [4]int{0, 10, 22, 60}},
}

// Track is one physical track on one head of the disk. Storage layout:
//
//	data:   bpt bytes — what the FDC head reads
//	clocks: bpt/8 bytes — clock-mark bitmap (1 = missing clock pattern)
//	weak:   bpt/8 bytes — weak-data bitmap (1 = weak byte, read random)
//
// The track's logical metadata (cylinder, head, sector size) lives in
// the fields below; bpt and the byte stream are the actual hardware view.
type Track struct {
	C      byte // Physical cylinder index (matches the disk geometry)
	H      byte // Physical head index
	N      byte // "Default" sector size code for the track (informational)
	Filler byte // Filler byte that was used to format the track

	bpt    int    // Bytes per track (length of data, clocks, weak)
	data   []byte // Track byte stream
	clocks []byte // Clock-mark bitmap (1 bit per byte of data)
	weak   []byte // Weak-data bitmap
}

// newTrack allocates a Track with the given byte capacity and zeroes the
// data area to the specified filler byte. clocks and weak start cleared.
func newTrack(bpt int, filler byte, c, h, n byte) *Track {
	bitmapLen := bitmapBytes(bpt)
	t := &Track{
		C:      c,
		H:      h,
		N:      n,
		Filler: filler,
		bpt:    bpt,
		data:   make([]byte, bpt),
		clocks: make([]byte, bitmapLen),
		weak:   make([]byte, bitmapLen),
	}
	for i := range t.data {
		t.data[i] = filler
	}
	return t
}

// bitmapBytes returns the number of bytes needed for a 1-bit-per-byte
// bitmap covering bpt bytes.
func bitmapBytes(bpt int) int {
	return (bpt + 7) / 8
}

// sectorLength returns the byte length of a sector with size code n
// (µPD765 "N" parameter). Real floppies cap sector sizes at 128 << 8 =
// 32 KB; attacker-controlled DSK/EDSK images could otherwise pass a
// larger N and trigger integer overflow in `128 << n` (n=56 wraps to a
// large negative int64, n=57+ wraps to zero). Clamping here keeps every
// caller safe with a single source of truth.
func sectorLength(n byte) int {
	if n > 8 {
		n = 8
	}
	return 128 << n
}

// bitSet sets bit i in the bitmap.
func bitSet(bm []byte, i int) {
	bm[i/8] |= 1 << uint(i%8)
}

// bitTest returns true if bit i in the bitmap is set.
func bitTest(bm []byte, i int) bool {
	return bm[i/8]&(1<<uint(i%8)) != 0
}

// bitClear clears bit i in the bitmap.
func bitClear(bm []byte, i int) {
	bm[i/8] &^= 1 << uint(i%8)
}

// trackBuilder assembles a Track's byte stream region by region: pre-
// index gap, sync, index mark, then per-sector ID and data fields, and
// finally the trailing gap. pos is the write head; all builder methods
// return false if the track would overflow.
type trackBuilder struct {
	t   *Track
	gap gapKind
	pos int
}

// newTrackBuilder wraps a freshly-created Track for byte-stream assembly.
func newTrackBuilder(t *Track, gap gapKind) *trackBuilder {
	return &trackBuilder{t: t, gap: gap}
}

// remaining returns the number of bytes free in the track.
func (b *trackBuilder) remaining() int {
	return b.t.bpt - b.pos
}

// emitFiller writes n copies of the gap filler byte. No clock marks set.
func (b *trackBuilder) emitFiller(n int) bool {
	if b.remaining() < n {
		return false
	}
	g := gapSpecs[b.gap]
	for i := 0; i < n; i++ {
		b.t.data[b.pos] = g.gap
		b.pos++
	}
	return true
}

// emitSync writes the sync field (the gap's syncLen sync bytes).
func (b *trackBuilder) emitSync() bool {
	g := gapSpecs[b.gap]
	if b.remaining() < g.syncLen {
		return false
	}
	for i := 0; i < g.syncLen; i++ {
		b.t.data[b.pos] = g.sync
		b.pos++
	}
	return true
}

// emitMark writes the 3×A1 address mark prefix (MFM only) with clock
// bits set on each. For FM the prefix is empty and the caller sets the
// clock bit on the actual mark byte directly.
func (b *trackBuilder) emitMark() bool {
	g := gapSpecs[b.gap]
	if g.mark < 0 {
		return true
	}
	if b.remaining() < 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		b.t.data[b.pos] = byte(g.mark)
		bitSet(b.t.clocks, b.pos)
		b.pos++
	}
	return true
}

// preindexAdd writes the pre-index gap, sync, mark, and the index mark
// (0xFC).
func (b *trackBuilder) preindexAdd() bool {
	g := gapSpecs[b.gap]
	if !b.emitFiller(g.len[0]) {
		return false
	}
	if !b.emitSync() {
		return false
	}
	if !b.emitMark() {
		return false
	}
	if b.remaining() < 1 {
		return false
	}
	if g.mark < 0 {
		bitSet(b.t.clocks, b.pos)
	}
	b.t.data[b.pos] = indexMark
	b.pos++
	return true
}

// postindexAdd writes the post-index gap (between the index mark and the
// first sector ID).
func (b *trackBuilder) postindexAdd() bool {
	g := gapSpecs[b.gap]
	return b.emitFiller(g.len[1])
}

// idAdd writes a sector ID field: sync, A1 mark, FE mark, CHRN, CRC,
// then GAP II. crcError corrupts the CRC bytes so the FDC's CRC check
// fails — used to model imaging-time errors recorded in EDSK ST1 bits.
func (b *trackBuilder) idAdd(c, h, r, n byte, crcError bool) bool {
	g := gapSpecs[b.gap]
	if !b.emitSync() {
		return false
	}
	if !b.emitMark() {
		return false
	}
	if b.remaining() < 7 {
		return false
	}
	crc := uint16(0xFFFF)
	if g.mark >= 0 {
		// CRC includes the three A1 prefix bytes for MFM.
		crc = crcUpdate(crc, byte(g.mark))
		crc = crcUpdate(crc, byte(g.mark))
		crc = crcUpdate(crc, byte(g.mark))
	} else {
		bitSet(b.t.clocks, b.pos)
	}
	b.t.data[b.pos] = idMark
	b.pos++
	crc = crcUpdate(crc, idMark)

	for _, by := range [4]byte{c, h, r, n} {
		b.t.data[b.pos] = by
		b.pos++
		crc = crcUpdate(crc, by)
	}
	if crcError {
		crc ^= 1 // flip LSB to corrupt the stored CRC
	}
	b.t.data[b.pos] = byte(crc >> 8)
	b.pos++
	b.t.data[b.pos] = byte(crc & 0xFF)
	b.pos++
	return b.emitFiller(g.len[2])
}

// dataAdd writes a sector data field: sync, A1 prefix, FB/F8 mark, data
// bytes, CRC, then GAP III. crcError corrupts the data CRC. Returns the
// position of the first data byte and a success flag — callers can use
// the position to mark a region weak after the fact.
func (b *trackBuilder) dataAdd(data []byte, deleted, crcError bool) (dataStart int, ok bool) {
	g := gapSpecs[b.gap]
	mark := byte(dataMark)
	if deleted {
		mark = delDataMrk
	}
	if !b.emitSync() {
		return 0, false
	}
	if !b.emitMark() {
		return 0, false
	}
	if b.remaining() < 1+len(data)+2+g.len[3] {
		return 0, false
	}
	if g.mark < 0 {
		bitSet(b.t.clocks, b.pos)
	}
	b.t.data[b.pos] = mark
	b.pos++

	crc := uint16(0xFFFF)
	if g.mark >= 0 {
		crc = crcUpdate(crc, byte(g.mark))
		crc = crcUpdate(crc, byte(g.mark))
		crc = crcUpdate(crc, byte(g.mark))
	}
	crc = crcUpdate(crc, mark)

	dataStart = b.pos
	for _, by := range data {
		b.t.data[b.pos] = by
		b.pos++
		crc = crcUpdate(crc, by)
	}
	if crcError {
		crc ^= 1
	}
	b.t.data[b.pos] = byte(crc >> 8)
	b.pos++
	b.t.data[b.pos] = byte(crc & 0xFF)
	b.pos++
	if !b.emitFiller(g.len[3]) {
		return dataStart, false
	}
	return dataStart, true
}

// gap4Add fills the rest of the track with the gap byte.
func (b *trackBuilder) gap4Add() bool {
	if b.remaining() < 0 {
		return false
	}
	g := gapSpecs[b.gap]
	for b.pos < b.t.bpt {
		b.t.data[b.pos] = g.gap
		b.pos++
	}
	return true
}

// markWeakRange marks a range of bytes as weak. Reads of weak bytes return
// random data each time, modelling copy-protected sectors.
func (b *trackBuilder) markWeakRange(start, length int) {
	for i := start; i < start+length && i < b.t.bpt; i++ {
		bitSet(b.t.weak, i)
	}
}

// crcUpdate is the CRC-16-CCITT update used by the µPD765's address-mark
// and data-field CRCs. Polynomial 0x1021, initial value 0xFFFF.
func crcUpdate(crc uint16, b byte) uint16 {
	crc ^= uint16(b) << 8
	for i := 0; i < 8; i++ {
		if crc&0x8000 != 0 {
			crc = (crc << 1) ^ 0x1021
		} else {
			crc <<= 1
		}
	}
	return crc
}

// IdAt is the exported entry point for walking a track's byte stream
// to find the next IDAM. Used by external packages (e.g. disciple)
// that implement their own FDC command logic.
func (t *Track) IdAt(start int) (c, h, r, n byte, idEnd int, ok bool) {
	return t.idAt(start)
}

// DataAt is the exported entry point for locating the data address
// mark following an IDAM. Returns the position of the first data byte.
func (t *Track) DataAt(start int) (dataStart int, deleted bool, ok bool) {
	return t.dataAt(start)
}

// Bytes returns a copy of the track's raw byte stream — the MFM-decoded
// track image (gaps, sync, address marks, sector ID + data fields, CRCs)
// as the WD177x Read Track command streams it to the host.
func (t *Track) Bytes() []byte {
	if t == nil {
		return nil
	}
	out := make([]byte, len(t.data))
	copy(out, t.data)
	return out
}

// WriteData overwrites the track byte stream starting at position pos
// with the given bytes. Used by peripheral packages that implement
// write-sector operations.
func (t *Track) WriteData(pos int, data []byte) {
	for i, b := range data {
		if pos+i >= t.bpt {
			break
		}
		t.data[pos+i] = b
	}
}

// readByte returns the byte at position i, substituting random data if
// the byte is marked weak. Used by the FDC's data transfer path; speed
// matters more than cryptographic randomness here.
func (t *Track) readByte(i int) byte {
	if i < 0 || i >= t.bpt {
		return 0xFF
	}
	if bitTest(t.weak, i) {
		return byte(rand.Uint32())
	}
	return t.data[i]
}

// idAt walks the track byte stream from position start, looking for an
// IDAM (sync mark + 0xFE with clock). Returns (cylinder, head, sector,
// size, idEndPos, true) on success — idEndPos is the position
// immediately after the CRC, where the data field's gap begins.
//
// crcOK is false when the on-track CRC bytes don't match a freshly
// computed CRC over the IDAM and CHRN. The walker still returns the
// CHRN values so the caller can decide whether to skip the sector
// (recording ST1.DE) or use it anyway.
func (t *Track) idAt(start int) (c, h, r, n byte, idEnd int, ok bool) {
	c, h, r, n, idEnd, _, ok = t.idAtCRC(start)
	return
}

// idAtCRC is the same as idAt but also returns whether the on-track CRC
// matches the computed CRC over the ID field. The walker is split into
// two entry points so callers that don't care about the CRC can stay
// minimal.
func (t *Track) idAtCRC(start int) (c, h, r, n byte, idEnd int, crcOK bool, ok bool) {
	a1Run := 0
	for i := start; i < t.bpt; i++ {
		if t.data[i] == a1Mark && bitTest(t.clocks, i) {
			a1Run++
			continue
		}
		if t.data[i] == idMark && (bitTest(t.clocks, i) || a1Run > 0) {
			if i+6 >= t.bpt {
				return 0, 0, 0, 0, 0, false, false
			}
			c = t.data[i+1]
			h = t.data[i+2]
			r = t.data[i+3]
			n = t.data[i+4]

			// Recompute the ID CRC. The CRC primer includes the
			// 3×A1 sync prefix (only on MFM tracks; FM tracks
			// signal the IDAM via the clock bit alone).
			crc := uint16(0xFFFF)
			if a1Run > 0 {
				crc = crcUpdate(crc, a1Mark)
				crc = crcUpdate(crc, a1Mark)
				crc = crcUpdate(crc, a1Mark)
			}
			crc = crcUpdate(crc, idMark)
			crc = crcUpdate(crc, c)
			crc = crcUpdate(crc, h)
			crc = crcUpdate(crc, r)
			crc = crcUpdate(crc, n)
			storedHi := t.data[i+5]
			storedLo := t.data[i+6]
			crcOK = storedHi == byte(crc>>8) && storedLo == byte(crc&0xFF)

			return c, h, r, n, i + 7, crcOK, true
		}
		a1Run = 0
	}
	return 0, 0, 0, 0, 0, false, false
}

// dataAt walks from start looking for a data address mark (FB or F8 with
// the A1 sync prefix or clock bit). Returns (dataStart, deleted, ok) —
// dataStart is the position of the first data byte after the mark.
//
// If a new IDAM (FE with clock) appears before any DAM, the search
// fails: the legitimate DAM for a sector always sits between its IDAM
// and the next sector's IDAM, so encountering an IDAM means the current
// sector has no data field (an IDAM-only sector — used by some
// copy-protection schemes and produced by EDSK images that record
// length=0). Without this guard, dataAt would walk into the *next*
// sector and silently return that sector's DAM and data instead of
// failing with ST1.MA / ST1.ND.
func (t *Track) dataAt(start int) (dataStart int, deleted bool, ok bool) {
	a1Run := 0
	for i := start; i < t.bpt; i++ {
		if t.data[i] == a1Mark && bitTest(t.clocks, i) {
			a1Run++
			continue
		}
		// A new IDAM means we've walked into the next sector's ID
		// field — give up before we can mistake the next sector's
		// DAM for this sector's.
		if t.data[i] == idMark && (bitTest(t.clocks, i) || a1Run > 0) {
			return 0, false, false
		}
		isMark := (t.data[i] == dataMark || t.data[i] == delDataMrk) &&
			(bitTest(t.clocks, i) || a1Run > 0)
		if isMark {
			deleted = (t.data[i] == delDataMrk)
			return i + 1, deleted, true
		}
		a1Run = 0
	}
	return 0, false, false
}
