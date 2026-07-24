package rzx

import "testing"

// Fuzz the RZX block walker against arbitrary bytes. A malformed recording
// (truncated block lengths, absurd frame counts, hostile zlib streams) must be
// rejected or parsed without panicking or inflating unbounded memory.
//
// Run: go test ./pkg/rzx/ -run x -fuzz FuzzRead -fuzztime 60s
func FuzzRead(f *testing.F) {
	f.Add([]byte("RZX!\x00\x0d\x00\x00\x00\x00"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Read(data)
	})
}
