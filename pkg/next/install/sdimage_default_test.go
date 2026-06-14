package install

import (
	"os"
	"path/filepath"
	"testing"
)

// A user running `zx_go --next` with no env/config should get a
// bootable card automatically when one is present at the standard
// install location (<install-dir>/sd.img). the development log: the
// FAT16 builder fallback is NOT bootable; users hit a black screen.
func TestSDCardImageDefaultsToInstallDirSDImg(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_SD_IMG", "")
	// Redirect the install dir to a temp dir. An earlier version of
	// this test used the REAL install dir (repo roms/next) and wrote
	// + deleted sd.img there — destroying the developer's actual
	// 1 GB card on every `go test ./pkg/next/install/` run (the
	// exact clobber install.Path()'s doc comment warns about).
	t.Setenv("ZX_GO_NEXT_ROM_DIR", t.TempDir())
	old := ConfiguredSDImage
	ConfiguredSDImage = ""
	t.Cleanup(func() { ConfiguredSDImage = old })

	dir, err := Path()
	if err != nil {
		t.Skipf("no install dir: %v", err)
	}
	img := filepath.Join(dir, "sd.img")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot write install dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(img) })

	if got := SDCardImage(); got != img {
		t.Errorf("SDCardImage() = %q, want %q (install-dir default)", got, img)
	}
}
