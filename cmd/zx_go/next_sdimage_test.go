package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildNextSDImage must serve the WHOLE card directory. Regression for
// issue #9: the GUI folder-mode build applied sdcard.NextBootFilter(),
// which stripped QXL.WIN, README.md, games/, docs/ and most other
// entries, leaving only 6 of the user's files visible in the NextZXOS
// Browser. The filter belongs to the bundled minimal boot card only,
// never to the user's own card.
func TestBuildNextSDImageServesFullTree(t *testing.T) {
	dir := t.TempDir()
	must := func(rel string, data []byte) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("TBBLUE.FW", []byte("fw"))       // kept by NextBootFilter
	must("QXL.WIN", []byte("ql-drive"))   // dropped by NextBootFilter
	must("games/game.nex", []byte("nex")) // games/ dropped by NextBootFilter

	img, err := buildNextSDImage(dir)
	if err != nil {
		t.Fatalf("buildNextSDImage: %v", err)
	}
	// Root directory entries carry the 11-byte space-padded 8.3 name.
	for _, name11 := range []string{"QXL     WIN", "GAMES      ", "TBBLUE  FW "} {
		if !bytes.Contains(img, []byte(name11)) {
			t.Errorf("image missing root entry %q — the full card tree must be served", name11)
		}
	}
}
