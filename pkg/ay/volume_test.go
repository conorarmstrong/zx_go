package ay

import "testing"

// TestVolumeCurveIsMeasured guards the measured AY-3-8912 volume curve:
// strictly monotonic, loudest level preserves mixing headroom, and the
// characteristic irregular steps (the 7→8 step is proportionally smaller
// than 8→9, which a uniform 3 dB-per-step approximation would not show).
func TestVolumeCurveIsMeasured(t *testing.T) {
	for i := 1; i < 16; i++ {
		if volumeTable[i] <= volumeTable[i-1] {
			t.Errorf("volumeTable not monotonic at %d: %d <= %d", i, volumeTable[i], volumeTable[i-1])
		}
	}
	if volumeTable[15] != 11650 {
		t.Errorf("loudest level = %d, want 11650 (mixing headroom)", volumeTable[15])
	}
	r78 := float64(volumeTable[8]) / float64(volumeTable[7])
	r89 := float64(volumeTable[9]) / float64(volumeTable[8])
	if r78 >= r89 {
		t.Errorf("expected measured kink: ratio(7→8)=%.3f should be < ratio(8→9)=%.3f", r78, r89)
	}
}
