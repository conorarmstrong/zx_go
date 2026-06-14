package sdcard

import (
	"fmt"
	"os"
)

// ImageSource is a BlockSource backed by an in-memory []byte image.
type ImageSource struct {
	data     []byte
	readOnly bool
	dirty    bool
}

// NewImageSource wraps an image-bytes slice as a block source.
// The image length must be a multiple of 512.
func NewImageSource(data []byte, readOnly bool) (*ImageSource, error) {
	if len(data)%512 != 0 {
		return nil, fmt.Errorf("sdcard: image length %d is not a multiple of 512", len(data))
	}
	return &ImageSource{data: data, readOnly: readOnly}, nil
}

// ReadBlock copies 512 bytes for the given LBA into dst.
func (s *ImageSource) ReadBlock(lba uint32, dst []byte) error {
	off := int(lba) * 512
	if off+512 > len(s.data) {
		// Beyond image: return zeros. Real cards return errors,
		// but NextZXOS's bootloader tolerates this and we don't
		// want a spurious "card error" stopping the boot if the
		// host probes past EOC.
		for i := range dst {
			dst[i] = 0
		}
		return nil
	}
	copy(dst, s.data[off:off+512])
	return nil
}

// WriteBlock writes 512 bytes into the image at the given LBA.
// Returns an error if the source is read-only.
func (s *ImageSource) WriteBlock(lba uint32, src []byte) error {
	if s.readOnly {
		return fmt.Errorf("sdcard: image is read-only")
	}
	off := int(lba) * 512
	if off+512 > len(s.data) {
		return fmt.Errorf("sdcard: write LBA %d beyond image", lba)
	}
	copy(s.data[off:off+512], src)
	s.dirty = true
	return nil
}

// Dirty reports whether any guest write has landed in the image
// since load (or the last WriteBackTo). Drives the opt-in
// write-back policy: an untouched card is never rewritten.
func (s *ImageSource) Dirty() bool { return s.dirty }

// WriteBackTo persists the in-memory image to path. The previous
// file (if any) is first renamed to path+".bak" so an interrupted
// write cannot destroy the only copy of the card. Clears the dirty
// flag on success.
func (s *ImageSource) WriteBackTo(path string) error {
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".bak"); err != nil {
			return fmt.Errorf("sdcard: backup %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, s.data, 0o644); err != nil {
		return fmt.Errorf("sdcard: write-back %s: %w", path, err)
	}
	s.dirty = false
	return nil
}

// Capacity returns the number of 512-byte blocks in the image.
func (s *ImageSource) Capacity() uint32 {
	return uint32(len(s.data) / 512)
}

// Bytes returns the underlying image data. Tests and callers that
// want to inspect contents (e.g. for verifying a NextZXOS write
// landed at the right LBA) use this.
func (s *ImageSource) Bytes() []byte { return s.data }
