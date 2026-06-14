package disciple

import "testing"

// TestParseFormatStream covers the Write Track format-stream parser: an
// IDAM (0xFE + C,H,R,N) followed by a DAM (0xFB) + 128<<N data bytes.
func TestParseFormatStream(t *testing.T) {
	var stream []byte
	stream = append(stream, 0x4E, 0x4E)       // gap
	stream = append(stream, 0xFE, 0, 0, 5, 1) // IDAM: C=0 H=0 R=5 N=1 (256 bytes)
	stream = append(stream, 0x4E, 0x4E)       // gap
	stream = append(stream, 0xFB)             // DAM
	data := make([]byte, 256)
	for i := range data {
		data[i] = 0xCC
	}
	stream = append(stream, data...)
	stream = append(stream, 0x4E) // trailing gap

	secs := parseFormatStream(stream)
	if len(secs) != 1 {
		t.Fatalf("parsed %d sectors, want 1", len(secs))
	}
	s := secs[0]
	if s.C != 0 || s.H != 0 || s.R != 5 || s.N != 1 {
		t.Errorf("sector ID = C%d H%d R%d N%d, want 0/0/5/1", s.C, s.H, s.R, s.N)
	}
	if len(s.Data) != 256 || s.Data[0] != 0xCC {
		t.Errorf("sector data: len=%d [0]=%#x, want 256 / 0xCC", len(s.Data), s.Data[0])
	}
}
