package snapshot

import "testing"

// Fuzz the snapshot parsers against arbitrary bytes: a malformed .sna/.z80/.szx
// (truncated, bad block lengths, absurd register banks) must return an error,
// never panic — a public emulator will be handed corrupt and hostile files.
//
// Run: go test ./pkg/snapshot/ -run x -fuzz FuzzLoadBytes -fuzztime 30s
func FuzzLoadBytes(f *testing.F) {
	// Seed with a few valid-ish snapshots so the fuzzer starts from structure.
	for _, format := range []SnapshotFormat{FormatSNA, FormatZ80, FormatSZX} {
		if data, err := New().SaveBytes(format); err == nil {
			f.Add(int(format), data)
		}
	}
	f.Add(int(FormatSNA), []byte{})
	f.Add(int(FormatZ80), []byte{0x00})

	f.Fuzz(func(t *testing.T, formatN int, data []byte) {
		format := SnapshotFormat(formatN % int(FormatUnknown))
		s := New()
		// Must not panic. An error is the expected outcome for garbage.
		_ = s.LoadBytes(data, format)
	})
}
