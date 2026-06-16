package ula

import (
	"os"
	"path/filepath"
	"testing"
)

// Fuzz the TAP and TZX tape parsers against arbitrary bytes: a malformed tape
// (bad block lengths, truncated TZX blocks, absurd pilot counts) must be
// rejected or loaded without panicking. A subsequent Play + Update must also be
// crash-free, since the pulse generator runs on whatever blocks parsed.
//
// Run: go test ./pkg/ula/ -run x -fuzz FuzzLoadTAP -fuzztime 30s  (and FuzzLoadTZX)
func fuzzTape(t *testing.T, ext string, data []byte, load func(*TapePlayer, string) error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fuzz"+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := load(tp, path); err != nil {
		return // rejected — fine
	}
	// Exercise the pulse generator + block walk on the parsed blocks.
	tp.Play()
	for i := 0; i < 64 && tp.IsPlaying(); i++ {
		tp.Update(100000)
	}
}

func FuzzLoadTAP(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x13, 0x00, 0x00}) // length says 19 but block is short
	f.Add([]byte{0x02, 0x00, 0x00, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzTape(t, ".tap", data, (*TapePlayer).LoadTAP)
	})
}

func FuzzLoadTZX(f *testing.F) {
	f.Add([]byte("ZXTape!\x1a\x01\x14")) // valid magic + version, no blocks
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzTape(t, ".tzx", data, (*TapePlayer).LoadTZX)
	})
}
