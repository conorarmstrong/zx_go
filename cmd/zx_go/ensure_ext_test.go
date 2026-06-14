package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFileExt(t *testing.T) {
	dir := t.TempDir()

	// No extension → renamed to add .png.
	noExt := filepath.Join(dir, "shot")
	if err := os.WriteFile(noExt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ensureFileExt(noExt, ".png")
	if got != noExt+".png" {
		t.Errorf("ensureFileExt(no ext) = %q, want %q", got, noExt+".png")
	}
	if _, err := os.Stat(noExt + ".png"); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(noExt); !os.IsNotExist(err) {
		t.Error("original no-ext file should have been renamed away")
	}

	// Already has the extension (any case) → unchanged, no rename.
	for _, name := range []string{"a.png", "b.PNG", "c.PnG"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ensureFileExt(p, ".png"); got != p {
			t.Errorf("ensureFileExt(%q) = %q, want unchanged", name, got)
		}
	}

	// Rename collision (target exists) → returns original unchanged.
	src := filepath.Join(dir, "dup")
	dst := filepath.Join(dir, "dup.png")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	_ = os.WriteFile(dst, []byte("y"), 0o644)
	if got := ensureFileExt(src, ".png"); got != src {
		t.Errorf("ensureFileExt on collision = %q, want original %q", got, src)
	}
}
