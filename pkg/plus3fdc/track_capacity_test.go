package plus3fdc

import "testing"

// A double-density track physically holds about 6250 bytes, and the IBM
// System 34 gap layout we emit spends 54 of them on GAP III after every
// sector. That is the *nominal* layout; a real formatter packs more sectors
// onto a track by shortening GAP III, and publishers did exactly that.
//
// Ten 512-byte sectors need roughly 10*(116+512)+130 = 6410 bytes at nominal
// gaps, which overflows — so the builder refused the disk outright with
// "track too small to write sector 9 data". Found by screening a disk
// collection: ten images failed this way, including Adidas Championship
// Tie-Break (Ocean 1990) and Bonanza Bros (US Gold 1991).
//
// Refusing is the wrong answer. The data fits on the medium; only our gap
// choice does not.

// buildTrack lays out n sectors of the given size code and reports success.
func buildTrack(t *testing.T, sectors int, sizeCode byte) bool {
	t.Helper()
	tr := newTrack(bytesPerTrackDD, 0xE5, 0, 0, sizeCode)
	b := newTrackBuilder(tr, gapMFM)
	if !b.preindexAdd() || !b.postindexAdd() {
		t.Fatal("gap setup failed")
	}
	b.planSectors(sectors, sectorLength(sizeCode))
	data := make([]byte, sectorLength(sizeCode))
	for j := 0; j < sectors; j++ {
		if !b.idAdd(0, 0, byte(j+1), sizeCode, false) {
			return false
		}
		if _, ok := b.dataAdd(data, false, false); !ok {
			return false
		}
	}
	return true
}

// TestTrackFitsNineSectorsAtNominalGaps pins the standard +3 layout, which
// must keep using the nominal gaps.
func TestTrackFitsNineSectorsAtNominalGaps(t *testing.T) {
	if !buildTrack(t, 9, 2) {
		t.Error("the standard 9x512 +3 track no longer fits")
	}
}

// TestTrackFitsDenserFormats pins the formats that were being refused.
func TestTrackFitsDenserFormats(t *testing.T) {
	for _, tc := range []struct {
		sectors  int
		sizeCode byte
		name     string
	}{
		{10, 2, "10x512 (Adidas, Bonanza Bros)"},
		{18, 1, "18x256"},
		{17, 1, "17x256"},
	} {
		if !buildTrack(t, tc.sectors, tc.sizeCode) {
			t.Errorf("%s does not fit; the gap layout must tighten rather than refuse the disk", tc.name)
		}
	}
}

// TestTrackStillRefusesTheImpossible guards the other side: tightening gaps
// must not pretend a track can hold more data than the medium does.
func TestTrackStillRefusesTheImpossible(t *testing.T) {
	if buildTrack(t, 20, 2) {
		t.Error("20x512 = 10240 data bytes was accepted onto a 6250-byte track")
	}
}
