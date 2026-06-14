package sdcard

import (
	"bytes"
	"testing"
)

// ImageSource unit tests (iter 222) — the in-memory block backing
// for the SD-card model had no direct test coverage.

// TestNewImageSource_RejectsNon512Multiple verifies length validation.
func TestNewImageSource_RejectsNon512Multiple(t *testing.T) {
	for _, n := range []int{1, 511, 513, 1023} {
		_, err := NewImageSource(make([]byte, n), false)
		if err == nil {
			t.Errorf("NewImageSource(len=%d) returned no error", n)
		}
	}
}

// TestNewImageSource_AcceptsValidSizes — multiples of 512 are OK.
func TestNewImageSource_AcceptsValidSizes(t *testing.T) {
	for _, n := range []int{0, 512, 1024, 4096, 16 * 1024 * 1024} {
		src, err := NewImageSource(make([]byte, n), false)
		if err != nil {
			t.Errorf("NewImageSource(len=%d): %v", n, err)
		}
		if src == nil {
			t.Errorf("NewImageSource(len=%d) returned nil source", n)
		}
	}
}

// TestReadBlock_CopiesData.
func TestReadBlock_CopiesData(t *testing.T) {
	img := make([]byte, 2048)
	for i := range img {
		img[i] = byte(i)
	}
	src, _ := NewImageSource(img, false)
	dst := make([]byte, 512)
	if err := src.ReadBlock(1, dst); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	// Block 1 = bytes 512..1023.
	for i := 0; i < 512; i++ {
		want := byte(512 + i)
		if dst[i] != want {
			t.Errorf("dst[%d] = $%02X, want $%02X", i, dst[i], want)
			return
		}
	}
}

// TestReadBlock_BeyondImage_Zeros verifies the spec-tolerant
// "return zeros past EOC" behaviour.
func TestReadBlock_BeyondImage_Zeros(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 512), false)
	// Pre-fill dst with non-zero to detect proper zeroing.
	dst := bytes.Repeat([]byte{0xCC}, 512)
	if err := src.ReadBlock(1, dst); err != nil {
		t.Fatalf("ReadBlock beyond: %v", err)
	}
	for i, b := range dst {
		if b != 0 {
			t.Errorf("dst[%d] = $%02X, want 0 (zero past EOC)", i, b)
			return
		}
	}
}

// TestWriteBlock_RejectsReadOnly.
func TestWriteBlock_RejectsReadOnly(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 512), true)
	if err := src.WriteBlock(0, make([]byte, 512)); err == nil {
		t.Error("WriteBlock to read-only source returned no error")
	}
}

// TestWriteBlock_RejectsBeyondImage.
func TestWriteBlock_RejectsBeyondImage(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 512), false)
	if err := src.WriteBlock(1, make([]byte, 512)); err == nil {
		t.Error("WriteBlock beyond image returned no error")
	}
}

// TestWriteBlock_PersistsChanges verifies a successful write
// makes the bytes readable via Bytes() / ReadBlock.
func TestWriteBlock_PersistsChanges(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 1024), false)
	data := bytes.Repeat([]byte{0xA5}, 512)
	if err := src.WriteBlock(1, data); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	got := src.Bytes()
	for i := 0; i < 512; i++ {
		if got[512+i] != 0xA5 {
			t.Errorf("byte %d after WriteBlock(1) = $%02X, want $A5",
				512+i, got[512+i])
			return
		}
	}
	// Block 0 untouched.
	for i := 0; i < 512; i++ {
		if got[i] != 0 {
			t.Errorf("byte %d (block 0) = $%02X, want 0 (block 0 untouched)",
				i, got[i])
			return
		}
	}
}

// TestCapacity_Reports512ByteBlocks.
func TestCapacity_Reports512ByteBlocks(t *testing.T) {
	for _, n := range []int{0, 512, 1024, 4096} {
		src, _ := NewImageSource(make([]byte, n), false)
		want := uint32(n / 512)
		if got := src.Capacity(); got != want {
			t.Errorf("Capacity for %d-byte image = %d, want %d", n, got, want)
		}
	}
}

// TestBytes_ReturnsBackingArray.
func TestBytes_ReturnsBackingArray(t *testing.T) {
	img := make([]byte, 512)
	for i := range img {
		img[i] = byte(i)
	}
	src, _ := NewImageSource(img, false)
	got := src.Bytes()
	if &got[0] != &img[0] {
		t.Error("Bytes() didn't return the backing slice (defensive copy unexpected)")
	}
}
