package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

// DetectFormat's contract says it detects "based on file extension and
// content", but it only ever looked at the extension, so a snapshot under any
// other name was rejected outright as an unsupported format.
//
// That is not hypothetical: NextZXOS ships games whose snapshots carry a
// .snx extension and are byte-for-byte 48K .sna — a 27-byte header followed
// by 49152 bytes of RAM. Nothing could load them.
func TestDetectFormatFallsBackToContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if got := DetectFormat(write("game.snx", SNA48K_SIZE)); got != FormatSNA {
		t.Errorf("DetectFormat(48K-sized .snx) = %v, want FormatSNA", got)
	}
	if got := DetectFormat(write("game.snx", SNA128K_SIZE)); got != FormatSNA {
		t.Errorf("DetectFormat(128K-sized .snx) = %v, want FormatSNA", got)
	}
}

// TestDetectFormatKeepsTrustingTheExtension pins that a known extension still
// wins, so nothing about the existing paths changes.
func TestDetectFormatKeepsTrustingTheExtension(t *testing.T) {
	dir := t.TempDir()
	for name, want := range map[string]SnapshotFormat{
		"a.sna": FormatSNA, "a.z80": FormatZ80, "a.szx": FormatSZX,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte{1, 2, 3}, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := DetectFormat(p); got != want {
			t.Errorf("DetectFormat(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestDetectFormatStillRejectsUnknowns guards the other side: a size that
// matches nothing is still unknown, and a missing file must not panic.
func TestDetectFormatStillRejectsUnknowns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(p, make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DetectFormat(p); got != FormatUnknown {
		t.Errorf("DetectFormat(odd-sized .txt) = %v, want FormatUnknown", got)
	}
	if got := DetectFormat(filepath.Join(dir, "missing.snx")); got != FormatUnknown {
		t.Errorf("DetectFormat(missing file) = %v, want FormatUnknown", got)
	}
}
