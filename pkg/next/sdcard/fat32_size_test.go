package sdcard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecommendedFAT32SizeMB_FloorForSmallTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RecommendedFAT32SizeMB(dir); got != minFAT32SizeMB {
		t.Errorf("small tree size = %d MB, want floor %d", got, minFAT32SizeMB)
	}
}

func TestRecommendedFAT32SizeMB_GrowsToFitContent(t *testing.T) {
	dir := t.TempDir()
	// A 400 MB (sparse) file must push the recommended size above the
	// floor so the content actually fits — the whole point is that a
	// large card isn't silently truncated.
	f, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(400 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got := RecommendedFAT32SizeMB(dir)
	if got < 400 {
		t.Errorf("400 MB tree size = %d MB, want >= 400 to fit content", got)
	}
	if got > maxFAT32SizeMB {
		t.Errorf("size = %d MB exceeds cap %d", got, maxFAT32SizeMB)
	}
}

func TestRecommendedFAT32SizeMB_CapsHugeTree(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	// 8 GB sparse file — well past the 2 GB in-RAM cap.
	if err := f.Truncate(8 * 1024 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := RecommendedFAT32SizeMB(dir); got != maxFAT32SizeMB {
		t.Errorf("huge tree size = %d MB, want cap %d", got, maxFAT32SizeMB)
	}
}

// The folder-mode card must serve the WHOLE directory. NextBootFilter
// is a boot-time optimisation for the bundled minimal card; applying
// it to a user's own card hid QXL.WIN, games/, docs/ etc. from the
// Browser (issue #9). This asserts that with no SkipFile the builder
// keeps exactly those entries NextBootFilter would have dropped.
func TestBuildFAT32NoFilterKeepsWouldBeFilteredEntries(t *testing.T) {
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
	must("TBBLUE.FW", []byte("fw"))       // kept by the filter
	must("QXL.WIN", []byte("ql-drive"))   // dropped by the filter
	must("README.md", []byte("# readme")) // dropped by the filter
	must("games/game.nex", []byte("nex")) // games/ dropped by the filter
	must("docs/guide.txt", []byte("doc")) // docs/ dropped by the filter

	img, err := BuildFAT32(dir, FAT32Opts{SizeMB: 64})
	if err != nil {
		t.Fatalf("BuildFAT32: %v", err)
	}
	r := newFAT32Reader(t, img)
	for _, name := range []string{"TBBLUE.FW", "QXL.WIN", "README.md", "games", "docs"} {
		if r.find(r.rootClus, name) == nil {
			t.Errorf("root entry %q missing — the full card tree must be served", name)
		}
	}
}
