package audio

import "testing"

// The saturation policy, tested once now that there is one copy of it.
func TestClamp16Saturates(t *testing.T) {
	for _, tc := range []struct {
		in   int32
		want int16
	}{
		{0, 0}, {1, 1}, {-1, -1},
		{32767, 32767}, {-32768, -32768},
		{32768, 32767}, {-32769, -32768},
		{1 << 30, 32767}, {-(1 << 30), -32768},
	} {
		if got := Clamp16(tc.in); got != tc.want {
			t.Errorf("Clamp16(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The wrap this exists to prevent: without saturation 30000+8128 would come
// back as -27408, a full-scale pop at the loudest moment.
func TestSaturatingAdd16DoesNotWrap(t *testing.T) {
	if got := SaturatingAdd16(30000, 8128); got != 32767 {
		t.Errorf("SaturatingAdd16(30000, 8128) = %d, want 32767", got)
	}
	if got := SaturatingAdd16(-30000, -8192); got != -32768 {
		t.Errorf("SaturatingAdd16(-30000, -8192) = %d, want -32768", got)
	}
	if got := SaturatingAdd16(100, 50); got != 150 {
		t.Errorf("SaturatingAdd16(100, 50) = %d, want 150", got)
	}
}
