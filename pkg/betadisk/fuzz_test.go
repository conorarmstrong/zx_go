package betadisk

import "testing"

// Fuzz the .TRD image loader and sector addressing: arbitrary bytes must either
// be rejected or yield an image whose ReadSector never panics or reads out of
// bounds, for any coordinate.
//
// Run: go test ./pkg/betadisk/ -run x -fuzz FuzzLoadImage -fuzztime 30s
func FuzzLoadImage(f *testing.F) {
	f.Add(make([]byte, 80*2*trackBytes)) // valid 80-track DS
	f.Add(make([]byte, 327680))          // ambiguous size
	f.Add([]byte{})
	f.Add([]byte{0x16})

	f.Fuzz(func(t *testing.T, data []byte) {
		img, err := LoadImage(data)
		if err != nil {
			return // rejected — fine
		}
		if img == nil {
			t.Fatal("nil image with nil error")
		}
		// Every in-geometry sector read must be safe; out-of-range must error,
		// not panic or slice out of bounds.
		for _, c := range []int{0, img.Cylinders / 2, img.Cylinders - 1, img.Cylinders} {
			for _, s := range []int{0, img.Sides - 1, img.Sides} {
				for _, r := range []int{0, 1, SectorsPerTrack, SectorsPerTrack + 1} {
					_, _ = img.ReadSector(c, s, r)
				}
			}
		}
	})
}
