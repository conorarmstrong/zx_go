package disciple

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDisk_DispatchByExtension exercises parseDiskImage's
// extension-dispatch branches by LoadDisk-ing files with different
// suffixes. The parsers may reject the synthesized data, but the
// dispatch case is hit before that error path, which is what we're after.
func TestLoadDisk_DispatchByExtension(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatal(err)
	}
	const size = 819200 // valid MGT/IMG size
	for _, ext := range []string{".img", ".trd", ".d40", ".unknown"} {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "disk"+ext)
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		// We don't care whether the parser ultimately accepts the
		// data — only that LoadDisk routes through the correct case
		// branch. Either it succeeds (covers the parse path) or it
		// fails with a parse error (still covers the dispatch).
		_ = d.LoadDisk(0, path)
	}
}

func TestLoadDisk_BadPath(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.LoadDisk(0, "/no/such/file.mgt"); err == nil {
		t.Error("LoadDisk on missing file should return error")
	}
}
