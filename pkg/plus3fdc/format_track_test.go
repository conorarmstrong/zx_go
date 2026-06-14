package plus3fdc

import "testing"

// TestFormatTrackAndBytes covers FormatTrack (used by Write Track) and
// Track.Bytes (used by Read Track): format a track with one sector, read
// the sector back, and confirm the raw track image is non-empty + has an
// IDAM.
func TestFormatTrackAndBytes(t *testing.T) {
	d := &Disk{Sides: 1, Cylinders: 1, Tracks: [][]*Track{{nil}}}
	data := make([]byte, 512)
	for i := range data {
		data[i] = 0xA5
	}
	if err := d.FormatTrack(0, 0, []Sector{{C: 0, H: 0, R: 1, N: 2, Data: data}}); err != nil {
		t.Fatalf("FormatTrack: %v", err)
	}
	tr := d.Track(0, 0)
	if tr == nil {
		t.Fatal("no track after FormatTrack")
	}
	sec := tr.FindSector(1)
	if sec == nil {
		t.Fatal("sector R=1 not found after format")
	}
	if len(sec.Data) != 512 || sec.Data[0] != 0xA5 {
		t.Errorf("formatted sector: len=%d [0]=%#x, want 512 / 0xA5", len(sec.Data), sec.Data[0])
	}
	raw := tr.Bytes()
	if len(raw) == 0 {
		t.Fatal("Bytes() returned an empty track")
	}
	hasIDAM := false
	for _, b := range raw {
		if b == 0xFE {
			hasIDAM = true
			break
		}
	}
	if !hasIDAM {
		t.Error("raw track image has no IDAM (0xFE)")
	}
}
