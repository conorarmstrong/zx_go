package rzx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"strings"
	"testing"
)

// buildInputBlockRZX wraps a compressed frame stream in a minimal RZX file:
// the 10-byte header plus one 0x80 input-recording block declaring frameCount
// frames and the compressed flag.
func buildInputBlockRZX(t *testing.T, frameCount uint32, raw []byte) []byte {
	t.Helper()
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, 13)
	binary.LittleEndian.PutUint32(payload[0:4], frameCount)
	binary.LittleEndian.PutUint32(payload[9:13], irbFlagCompressed)
	payload = append(payload, z.Bytes()...)

	out := append([]byte(nil), magic...)
	out = append(out, 0x00, 0x0D, 0x00, 0x00, 0x00, 0x00)
	out = append(out, byte(BlockInput))
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(payload)+5))
	out = append(out, l[:]...)
	return append(out, payload...)
}

// A hostile RZX can declare one frame and carry a zlib stream that inflates to
// hundreds of megabytes. readFrames rejects the implausible frame count, but
// only AFTER the whole thing has been inflated into memory — so the read must
// be bounded by what the declared frame count could possibly need. The
// snapshot path next door already does this via zlibInflateLimit.
func TestInputBlockInflateIsBounded(t *testing.T) {
	// One frame can be at most 4 header bytes + 0xFFFF IN bytes.
	bomb := make([]byte, 4*(4+0xFFFF))
	data := buildInputBlockRZX(t, 1, bomb)

	_, err := Read(data)
	if err == nil {
		t.Fatal("a frame stream far larger than the declared frame count allows must be rejected")
	}
	if !strings.Contains(err.Error(), "inflate") && !strings.Contains(err.Error(), "implausible") {
		t.Errorf("unexpected error: %v", err)
	}
}

// The bound must not reject legitimate files: a stream exactly as large as the
// declared frames need still parses.
func TestInputBlockInflateAcceptsFullSizeFrames(t *testing.T) {
	const frames = 3
	var raw []byte
	for i := 0; i < frames; i++ {
		var hdr [4]byte
		binary.LittleEndian.PutUint16(hdr[0:2], 100)    // instruction count
		binary.LittleEndian.PutUint16(hdr[2:4], 0xFFFE) // near-max IN count
		raw = append(raw, hdr[:]...)
		raw = append(raw, make([]byte, 0xFFFE)...)
	}
	data := buildInputBlockRZX(t, frames, raw)

	f, err := Read(data)
	if err != nil {
		t.Fatalf("full-size frame stream rejected: %v", err)
	}
	if got := len(f.Blocks[0].Input.Frames); got != frames {
		t.Errorf("frames = %d, want %d", got, frames)
	}
}
